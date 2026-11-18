// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package prototext_test

import (
	"bytes"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	pb2 "google.golang.org/protobuf/internal/testprotos/textpb2"
	_ "google.golang.org/protobuf/internal/testprotos/textpb3" // registers pb3.Nested
)

func marshalDeterministic(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.MarshalOptions{AllowPartial: true, Deterministic: true}.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal(%T): %v", m, err)
	}
	return b
}

func anyOf(t *testing.T, m proto.Message) *anypb.Any {
	t.Helper()
	return &anypb.Any{
		TypeUrl: "type.googleapis.com/" + string(m.ProtoReflect().Descriptor().FullName()),
		Value:   marshalDeterministic(t, m),
	}
}

// nestAnyText builds a chain of depth Any messages around a StringValue.
// depth 1 is the StringValue Any itself.
func nestAnyText(depth int) []byte {
	const (
		anyURL = "type.googleapis.com/google.protobuf.Any"
		strURL = "type.googleapis.com/google.protobuf.StringValue"
	)
	var b strings.Builder
	for i := 0; i < depth-1; i++ {
		b.WriteByte('[')
		b.WriteString(anyURL)
		b.WriteString("]:{")
	}
	b.WriteByte('[')
	b.WriteString(strURL)
	b.WriteString("]:{value:\"hi\"}")
	for i := 0; i < depth-1; i++ {
		b.WriteByte('}')
	}
	return []byte(b.String())
}

func referenceNestedAny(t *testing.T, depth int) *anypb.Any {
	t.Helper()
	var m proto.Message = &wrapperspb.StringValue{Value: "hi"}
	for i := 0; i < depth; i++ {
		m = anyOf(t, m)
	}
	return m.(*anypb.Any)
}

func TestNestedAnyRoundTrip(t *testing.T) {
	for _, depth := range []int{1, 2, 3, 8} {
		t.Run(strings.Repeat("any/", depth), func(t *testing.T) {
			want := referenceNestedAny(t, depth)
			got := &anypb.Any{}
			if err := prototext.Unmarshal(nestAnyText(depth), got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.GetTypeUrl() != want.GetTypeUrl() || !bytes.Equal(got.GetValue(), want.GetValue()) {
				t.Fatalf("decoded Any bytes differ:\n got type %q value %x\nwant type %q value %x",
					got.GetTypeUrl(), got.GetValue(), want.GetTypeUrl(), want.GetValue())
			}

			gotText, err := prototext.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			wantText, err := prototext.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotText, wantText) {
				t.Fatalf("round-trip text differs:\n got %s\nwant %s", gotText, wantText)
			}
			again := &anypb.Any{}
			if err := prototext.Unmarshal(wantText, again); err != nil {
				t.Fatalf("Unmarshal(Marshal): %v", err)
			}
			if again.GetTypeUrl() != want.GetTypeUrl() || !bytes.Equal(again.GetValue(), want.GetValue()) {
				t.Fatalf("Marshal output decoded differently:\n got %x\nwant %x", again.GetValue(), want.GetValue())
			}
		})
	}

	// An Any field inside a normal message, itself wrapped in an Any, still
	// round-trips to the same value bytes. The outer message is marshaled
	// with proto.Marshal; the nested Any chain is not.
	t.Run("message containing nested Any", func(t *testing.T) {
		inner := &pb2.KnownTypes{OptAny: referenceNestedAny(t, 4)}
		want := anyOf(t, inner)
		text, err := prototext.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		got := &anypb.Any{}
		if err := prototext.Unmarshal(text, got); err != nil {
			t.Fatal(err)
		}
		if got.GetTypeUrl() != want.GetTypeUrl() || !bytes.Equal(got.GetValue(), want.GetValue()) {
			t.Fatalf("round-trip bytes differ:\n got %x\nwant %x", got.GetValue(), want.GetValue())
		}
		gotText, err := prototext.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gotText, text) {
			t.Fatalf("round-trip text differs:\n got %s\nwant %s", gotText, text)
		}
	})

	// Framing must match proto.Marshal when type_url or value is omitted,
	// and field order in the text must not change the wire bytes.
	t.Run("partial Any fields", func(t *testing.T) {
		cases := []struct {
			name string
			text string
			want *anypb.Any
		}{{
			name: "value only",
			text: `[type.googleapis.com/google.protobuf.Any]:{value:"abc"}`,
			want: anyOf(t, &anypb.Any{Value: []byte("abc")}),
		}, {
			name: "type_url only",
			text: `[type.googleapis.com/google.protobuf.Any]:{type_url:"foo/Bar"}`,
			want: anyOf(t, &anypb.Any{TypeUrl: "foo/Bar"}),
		}, {
			name: "empty",
			text: `[type.googleapis.com/google.protobuf.Any]:{}`,
			want: &anypb.Any{TypeUrl: "type.googleapis.com/google.protobuf.Any"},
		}, {
			name: "type_url then value",
			text: "[type.googleapis.com/google.protobuf.Any]:{type_url:\"foo/Bar\" value:\"abc\"}",
			want: anyOf(t, &anypb.Any{TypeUrl: "foo/Bar", Value: []byte("abc")}),
		}, {
			name: "value then type_url",
			text: "[type.googleapis.com/google.protobuf.Any]:{value:\"abc\" type_url:\"foo/Bar\"}",
			want: anyOf(t, &anypb.Any{TypeUrl: "foo/Bar", Value: []byte("abc")}),
		}}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := &anypb.Any{}
				if err := prototext.Unmarshal([]byte(tc.text), got); err != nil {
					t.Fatal(err)
				}
				if got.GetTypeUrl() != tc.want.GetTypeUrl() || !bytes.Equal(got.GetValue(), tc.want.GetValue()) {
					t.Fatalf("got type %q value %x\nwant type %q value %x",
						got.GetTypeUrl(), got.GetValue(), tc.want.GetTypeUrl(), tc.want.GetValue())
				}
				if tc.name == "empty" && got.Value != nil {
					t.Fatalf("empty Any value = %q, want unset", got.Value)
				}
			})
		}
	})

	t.Run("errors", func(t *testing.T) {
		cases := []struct {
			name    string
			text    string
			wantErr string
		}{{
			name: "nested invalid UTF-8",
			text: `[type.googleapis.com/google.protobuf.Any]:{
  [pb3.Nested]: { s_string: "abc\xff" }
}`,
			wantErr: "contains invalid UTF-8",
		}, {
			name:    "nested unresolved type",
			text:    `[type.googleapis.com/google.protobuf.Any]:{ [NoSuchMessage]:{} }`,
			wantErr: "unable to resolve message [NoSuchMessage]",
		}}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				err := prototext.Unmarshal([]byte(tc.text), &anypb.Any{})
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want substring %q", err, tc.wantErr)
				}
			})
		}
	})

	// depth Any messages plus the innermost StringValue are depth+1 messages.
	// A non-Any embed is counted once by unmarshalMessage.
	t.Run("recursion limit", func(t *testing.T) {
		const depth = 4
		raw := nestAnyText(depth)
		if err := (prototext.UnmarshalOptions{RecursionLimit: depth + 1}).Unmarshal(raw, &anypb.Any{}); err != nil {
			t.Fatalf("RecursionLimit %d: %v", depth+1, err)
		}
		err := (prototext.UnmarshalOptions{RecursionLimit: depth}).Unmarshal(raw, &anypb.Any{})
		if err == nil || !strings.Contains(err.Error(), "exceeded maximum recursion depth") {
			t.Fatalf("RecursionLimit %d: got %v", depth, err)
		}

		nested := `[pb2.Nested]:{ opt_string: "x" }`
		if err := (prototext.UnmarshalOptions{RecursionLimit: 2}).Unmarshal([]byte(nested), &anypb.Any{}); err != nil {
			t.Fatalf("RecursionLimit 2: %v", err)
		}
		err = (prototext.UnmarshalOptions{RecursionLimit: 1}).Unmarshal([]byte(nested), &anypb.Any{})
		if err == nil || !strings.Contains(err.Error(), "exceeded maximum recursion depth") {
			t.Fatalf("RecursionLimit 1: got %v", err)
		}
	})
}

// nestedAnyCost is the per-unmarshal time and bytes allocated, measured over
// enough iterations that the clock reading is not a single short sample.
type nestedAnyCost struct {
	ns    float64
	bytes float64
}

func measureNestedAny(t *testing.T, raw []byte) nestedAnyCost {
	t.Helper()
	if err := prototext.Unmarshal(raw, &anypb.Any{}); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	n := 1
	var elapsed time.Duration
	var alloc uint64
	for {
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		for i := 0; i < n; i++ {
			if err := prototext.Unmarshal(raw, &anypb.Any{}); err != nil {
				t.Fatal(err)
			}
		}
		elapsed = time.Since(start)
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		alloc = after.TotalAlloc - before.TotalAlloc
		if elapsed >= 40*time.Millisecond || n >= 20000 {
			break
		}
		n *= 2
	}
	return nestedAnyCost{ns: float64(elapsed.Nanoseconds()) / float64(n), bytes: float64(alloc) / float64(n)}
}

func median3(a, b, c float64) float64 {
	if a > b {
		a, b = b, a
	}
	if b > c {
		b, c = c, b
	}
	if a > b {
		a, b = b, a
	}
	return b
}

// TestNestedAnyDecodeLinear fails if decoding a chain of Any messages becomes
// superlinear again. A quadratic re-marshal is about 4x time and allocs when
// the depth doubles; linear growth stays near 2x. Depths are small enough
// that the check finishes quickly. The ratio is the median of several samples
// so one noisy clock reading does not fail the test.
func TestNestedAnyDecodeLinear(t *testing.T) {
	const shallowDepth = 200
	const deepDepth = 400
	const maxRatio = 2.8

	// GC work during the timed section makes short samples jump around.
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)

	shallowRaw := nestAnyText(shallowDepth)
	deepRaw := nestAnyText(deepDepth)
	var timeRatios, byteRatios [3]float64
	var shallow, deep nestedAnyCost
	for i := 0; i < 3; i++ {
		shallow = measureNestedAny(t, shallowRaw)
		deep = measureNestedAny(t, deepRaw)
		timeRatios[i] = deep.ns / shallow.ns
		byteRatios[i] = deep.bytes / shallow.bytes
	}
	timeRatio := median3(timeRatios[0], timeRatios[1], timeRatios[2])
	byteRatio := median3(byteRatios[0], byteRatios[1], byteRatios[2])
	t.Logf("depth %d → %d: time %.0f→%.0f ns (×%.2f, samples %.2f %.2f %.2f), alloc %.0f→%.0f B (×%.2f, samples %.2f %.2f %.2f)",
		shallowDepth, deepDepth,
		shallow.ns, deep.ns, timeRatio, timeRatios[0], timeRatios[1], timeRatios[2],
		shallow.bytes, deep.bytes, byteRatio, byteRatios[0], byteRatios[1], byteRatios[2])
	if timeRatio > maxRatio || byteRatio > maxRatio {
		t.Fatalf("depth doubled and cost grew superlinearly: time ×%.2f, alloc ×%.2f (limit ×%.1f)", timeRatio, byteRatio, maxRatio)
	}
}

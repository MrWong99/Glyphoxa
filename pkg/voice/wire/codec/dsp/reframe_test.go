package dsp

import "testing"

func TestReframer_RegroupsToFixedSize(t *testing.T) {
	// 320-sample pushes into a 512-sample reframer: classic inbound case. After
	// two pushes (640 samples) one 512 frame emerges, 128 left buffered.
	r := NewReframer(512)

	if frames := r.Push(make([]int16, 320)); len(frames) != 0 {
		t.Fatalf("first 320: got %d frames, want 0 (buffered)", len(frames))
	}
	if r.Buffered() != 320 {
		t.Fatalf("buffered after first push = %d, want 320", r.Buffered())
	}
	frames := r.Push(make([]int16, 320))
	if len(frames) != 1 || len(frames[0]) != 512 {
		t.Fatalf("second 320: got %d frames (first len %d), want one 512-frame", len(frames), frameLen(frames))
	}
	if r.Buffered() != 128 {
		t.Fatalf("buffered after second push = %d, want 128", r.Buffered())
	}
}

func TestReframer_MultipleFramesFromOnePush(t *testing.T) {
	r := NewReframer(512)
	frames := r.Push(make([]int16, 512*3+100)) // 3 full frames + remainder
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	for i, f := range frames {
		if len(f) != 512 {
			t.Fatalf("frame %d len = %d, want 512", i, len(f))
		}
	}
	if r.Buffered() != 100 {
		t.Fatalf("buffered = %d, want 100", r.Buffered())
	}
}

func TestReframer_PreservesSampleOrder(t *testing.T) {
	r := NewReframer(4)
	in := []int16{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	var got []int16
	for _, f := range r.Push(in) {
		got = append(got, f...)
	}
	for i := range got {
		if got[i] != int16(i) {
			t.Fatalf("reordered: got[%d]=%d", i, got[i])
		}
	}
}

func TestReframer_FlushZeroPadsTail(t *testing.T) {
	r := NewReframer(512)
	r.Push(make([]int16, 100))
	tail := r.Flush()
	if len(tail) != 512 {
		t.Fatalf("flush len = %d, want 512 (zero-padded)", len(tail))
	}
	for i := 100; i < 512; i++ {
		if tail[i] != 0 {
			t.Fatalf("flush[%d] = %d, want 0 (padding)", i, tail[i])
		}
	}
	if r.Flush() != nil {
		t.Fatal("second flush should be nil (buffer drained)")
	}
}

func TestReframer_FlushEmptyIsNil(t *testing.T) {
	if NewReframer(512).Flush() != nil {
		t.Fatal("flush of empty reframer should be nil")
	}
}

func frameLen(frames [][]int16) int {
	if len(frames) == 0 {
		return -1
	}
	return len(frames[0])
}

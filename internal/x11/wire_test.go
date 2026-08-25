// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestPad4(t *testing.T) {
	for _, tc := range []struct{ in, pad, want int }{
		{0, 0, 0}, {1, 3, 4}, {2, 2, 4}, {3, 1, 4}, {4, 0, 4}, {5, 3, 8}, {17, 3, 20},
	} {
		if got := pad4(tc.in); got != tc.want {
			t.Errorf("pad4(%d) = %d, want %d", tc.in, got, tc.want)
		}
		if got := padding(tc.in); got != tc.pad {
			t.Errorf("padding(%d) = %d, want %d", tc.in, got, tc.pad)
		}
	}
}

func TestEncoderBothOrders(t *testing.T) {
	for _, order := range []ByteOrder{binary.LittleEndian, binary.BigEndian} {
		e := newEncoder(order)
		e.put8(0xa1)
		e.put16(0x1234)
		e.put32(0xdeadbeef)
		e.putBytes([]byte{1, 2})
		e.putString("abc") // 3 bytes + 1 pad
		e.skip(2)
		want := []byte{0xa1}
		var b2 [2]byte
		order.PutUint16(b2[:], 0x1234)
		want = append(want, b2[:]...)
		var b4 [4]byte
		order.PutUint32(b4[:], 0xdeadbeef)
		want = append(want, b4[:]...)
		want = append(want, 1, 2, 'a', 'b', 'c', 0, 0, 0)
		if !bytes.Equal(e.buf, want) {
			t.Errorf("%v: encoded % x, want % x", order, e.buf, want)
		}
	}
}

func TestEncoderPadOnAligned(t *testing.T) {
	e := newEncoder(binary.LittleEndian)
	e.putString("abcd") // already aligned: no pad
	if len(e.buf) != 4 {
		t.Fatalf("aligned putString wrote %d bytes, want 4", len(e.buf))
	}
}

func TestDecoderReadsWhatEncoderWrote(t *testing.T) {
	order := binary.BigEndian
	e := newEncoder(order)
	e.put8(9)
	e.put16(0xfffe) // reads back as -2 signed
	e.put32(7)
	e.putString("hi")
	e.putBytes([]byte{4, 5, 6})

	d := newDecoder(order, e.buf)
	if got := d.get8(); got != 9 {
		t.Errorf("get8 = %d", got)
	}
	if got := d.get16s(); got != -2 {
		t.Errorf("get16s = %d, want -2", got)
	}
	if got := d.get32(); got != 7 {
		t.Errorf("get32 = %d", got)
	}
	if got := d.getString(2); got != "hi" {
		t.Errorf("getString = %q", got)
	}
	if got := d.getBytes(3); !bytes.Equal(got, []byte{4, 5, 6}) {
		t.Errorf("getBytes = % x", got)
	}
	if !d.ok {
		t.Error("decoder went not-ok on a well-formed buffer")
	}
}

func TestDecoderTruncationIsSticky(t *testing.T) {
	d := newDecoder(binary.LittleEndian, []byte{1, 2})
	if got := d.get32(); got != 0 {
		t.Errorf("short get32 = %d, want 0", got)
	}
	if d.ok {
		t.Fatal("decoder stayed ok after a short read")
	}
	// Once not-ok, every subsequent read is a zero and never a panic.
	if d.get8() != 0 || d.get16() != 0 || d.get32() != 0 {
		t.Error("reads after truncation did not all report zero")
	}
	if d.getBytes(1) != nil {
		t.Error("getBytes after truncation returned data")
	}
	if d.getString(1) != "" {
		t.Error("getString after truncation returned data")
	}
	d.skip(1)
	if d.ok {
		t.Error("skip resurrected a not-ok decoder")
	}
}

func TestDecoderNegativeNeed(t *testing.T) {
	d := newDecoder(binary.LittleEndian, []byte{1, 2, 3, 4})
	if d.need(-1) {
		t.Fatal("need(-1) reported true")
	}
	if d.ok {
		t.Fatal("a negative need left the decoder ok")
	}
}

func TestDecoderShortReadsPerWidth(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		d := newDecoder(binary.LittleEndian, make([]byte, n))
		d.get16()
		d.get32()
		d.get8()
		d.get8() // exercises the 1-byte short path for n == 0 and n == 1
	}
}

func TestOrderFor(t *testing.T) {
	if o, ok := orderFor(orderLSB); !ok || o != binary.ByteOrder(binary.LittleEndian) {
		t.Errorf("orderFor('l') = %v, %v", o, ok)
	}
	if o, ok := orderFor(orderMSB); !ok || o != binary.ByteOrder(binary.BigEndian) {
		t.Errorf("orderFor('B') = %v, %v", o, ok)
	}
	if _, ok := orderFor('x'); ok {
		t.Error("orderFor('x') reported a known order")
	}
}

func TestReadFull(t *testing.T) {
	b := make([]byte, 4)
	if err := readFull(bytes.NewReader([]byte{1, 2, 3, 4}), b); err != nil {
		t.Fatalf("readFull: %v", err)
	}
	err := readFull(bytes.NewReader([]byte{1}), b)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short readFull reported %v, want ErrUnexpectedEOF", err)
	}
}

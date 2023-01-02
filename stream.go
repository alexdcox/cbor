// Copyright (c) Faye Amacker. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package cbor

import (
	"errors"
	"io"
	"reflect"
)

// Decoder reads and decodes CBOR values from io.Reader.
type Decoder struct {
	r         io.Reader
	d         decoder
	buf       []byte
	off       int // next read offset in buf
	bytesRead int
	readError error
}

// NewDecoder returns a new decoder that reads and decodes from r using
// the default decoding options.
func NewDecoder(r io.Reader) *Decoder {
	return defaultDecMode.NewDecoder(r)
}

// Decode reads CBOR value and decodes it into the value pointed to by v.
func (dec *Decoder) Decode(v interface{}) error {
	if len(dec.buf) == dec.off {
		if n, err := dec.read(); n == 0 {
			return err
		}
	}
	for {
		dec.d.reset(dec.buf[dec.off:])
		err := dec.d.value(v, true)
		// Increment dec.off even if err is not nil because
		// dec.d.off points to the next CBOR data item if current
		// CBOR data item is valid but failed to be decoded into v.
		// This allows next CBOR data item to be decoded in next
		// call to this function.
		dec.off += dec.d.off
		dec.bytesRead += dec.d.off
		if err == nil {
			return nil
		}
		if err != io.ErrUnexpectedEOF {
			return err
		}
		// Need to read more data.
		if n, err := dec.read(); n == 0 {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
	}
}

// Skip skips to the next CBOR data item (if there is any),
// otherwise it returns error such as io.EOF, io.UnexpectedEOF, etc.
func (dec *Decoder) Skip() error {
	if len(dec.buf) == dec.off {
		if n, err := dec.read(); n == 0 {
			return err
		}
	}
	for {
		dec.d.reset(dec.buf[dec.off:])
		err := dec.d.valid(true)
		if err == nil {
			// Only increment dec.off if current CBOR data item is valid.
			// If current data item is incomplete (io.ErrUnexpectedEOF),
			// we want to try again after reading more data.
			dec.off += dec.d.off
			dec.bytesRead += dec.d.off
			return nil
		}
		if err != io.ErrUnexpectedEOF {
			return err
		}
		// Need to read more data.
		if n, err := dec.read(); n == 0 {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
	}
}

// NumBytesRead returns the number of bytes read.
func (dec *Decoder) NumBytesRead() int {
	return dec.bytesRead
}

func (dec *Decoder) read() (int, error) {
	if dec.readError != nil {
		return 0, dec.readError
	}

	// Grow buf if needed.
	const minRead = 512
	if cap(dec.buf)-len(dec.buf)+dec.off < minRead {
		oldUnreadBuf := dec.buf[dec.off:]
		dec.buf = make([]byte, len(dec.buf)-dec.off, 2*cap(dec.buf)+minRead)
		dec.overwriteBuf(oldUnreadBuf)
	}

	// Copy unread data over read data and reset off to 0.
	if dec.off > 0 {
		dec.overwriteBuf(dec.buf[dec.off:])
	}

	// Read from reader and reslice buf.
	n, err := dec.r.Read(dec.buf[len(dec.buf):cap(dec.buf)])
	dec.buf = dec.buf[0 : len(dec.buf)+n]
	dec.readError = err
	return n, err
}

func (dec *Decoder) overwriteBuf(newBuf []byte) {
	n := copy(dec.buf, newBuf)
	dec.buf = dec.buf[:n]
	dec.off = 0
}

// Encoder writes CBOR values to io.Writer.
type Encoder struct {
	w          io.Writer
	em         *encMode
	indefTypes []cborType
}

// NewEncoder returns a new encoder that writes to w using the default encoding options.
func NewEncoder(w io.Writer) *Encoder {
	return defaultEncMode.NewEncoder(w)
}

// Encode writes the CBOR encoding of v.
func (enc *Encoder) Encode(v interface{}) error {
	if len(enc.indefTypes) > 0 && v != nil {
		indefType := enc.indefTypes[len(enc.indefTypes)-1]
		if indefType == cborTypeTextString {
			k := reflect.TypeOf(v).Kind()
			if k != reflect.String {
				return errors.New("cbor: cannot encode item type " + k.String() + " for indefinite-length text string")
			}
		} else if indefType == cborTypeByteString {
			t := reflect.TypeOf(v)
			k := t.Kind()
			if (k != reflect.Array && k != reflect.Slice) || t.Elem().Kind() != reflect.Uint8 {
				return errors.New("cbor: cannot encode item type " + k.String() + " for indefinite-length byte string")
			}
		}
	}

	buf := getEncoderBuffer()

	err := encode(buf, enc.em, reflect.ValueOf(v))
	if err == nil {
		_, err = enc.w.Write(buf.Bytes())
	}

	putEncoderBuffer(buf)
	return err
}

// StartIndefiniteByteString starts byte string encoding of indefinite length.
// Subsequent calls of (*Encoder).Encode() encodes definite length byte strings
// ("chunks") as one contiguous string until EndIndefinite is called.
func (enc *Encoder) StartIndefiniteByteString() error {
	return enc.startIndefinite(cborTypeByteString)
}

// StartIndefiniteTextString starts text string encoding of indefinite length.
// Subsequent calls of (*Encoder).Encode() encodes definite length text strings
// ("chunks") as one contiguous string until EndIndefinite is called.
func (enc *Encoder) StartIndefiniteTextString() error {
	return enc.startIndefinite(cborTypeTextString)
}

// StartIndefiniteArray starts array encoding of indefinite length.
// Subsequent calls of (*Encoder).Encode() encodes elements of the array
// until EndIndefinite is called.
func (enc *Encoder) StartIndefiniteArray() error {
	return enc.startIndefinite(cborTypeArray)
}

// StartIndefiniteMap starts array encoding of indefinite length.
// Subsequent calls of (*Encoder).Encode() encodes elements of the map
// until EndIndefinite is called.
func (enc *Encoder) StartIndefiniteMap() error {
	return enc.startIndefinite(cborTypeMap)
}

// EndIndefinite closes last opened indefinite length value.
func (enc *Encoder) EndIndefinite() error {
	if len(enc.indefTypes) == 0 {
		return errors.New("cbor: cannot encode \"break\" code outside indefinite length values")
	}
	_, err := enc.w.Write([]byte{0xff})
	if err == nil {
		enc.indefTypes = enc.indefTypes[:len(enc.indefTypes)-1]
	}
	return err
}

var cborIndefHeader = map[cborType][]byte{
	cborTypeByteString: {0x5f},
	cborTypeTextString: {0x7f},
	cborTypeArray:      {0x9f},
	cborTypeMap:        {0xbf},
}

func (enc *Encoder) startIndefinite(typ cborType) error {
	if enc.em.indefLength == IndefLengthForbidden {
		return &IndefiniteLengthError{typ}
	}
	_, err := enc.w.Write(cborIndefHeader[typ])
	if err == nil {
		enc.indefTypes = append(enc.indefTypes, typ)
	}
	return err
}

// RawMessage is a raw encoded CBOR value.
type RawMessage []byte

// MarshalCBOR returns m or CBOR nil if m is nil.
func (m RawMessage) MarshalCBOR() ([]byte, error) {
	if len(m) == 0 {
		return cborNil, nil
	}
	return m, nil
}

// UnmarshalCBOR creates a copy of data and saves to *m.
func (m *RawMessage) UnmarshalCBOR(data []byte) error {
	if m == nil {
		return errors.New("cbor.RawMessage: UnmarshalCBOR on nil pointer")
	}
	*m = append((*m)[0:0], data...)
	return nil
}

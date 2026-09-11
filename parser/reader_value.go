package parser

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
)

func (r *Reader) readDefinedValueToken(header core.ElementHeader) (Token, error) {
	if r.encapsulatedSink != nil && isPixelDataValueTag(header.Tag) {
		return Token{}, fmt.Errorf("%w: encoded frame streaming requires encapsulated integer Pixel Data", ErrFrameSink)
	}
	if r.encapsulatedSink != nil && len(r.seqDelimiters) == 0 && isExtendedTable(header.Tag) {
		if err := r.encapsulatedSink.CheckExtendedTable(header); err != nil {
			return Token{}, err
		}
	}
	if err := r.validateDefinedValueLength(header); err != nil {
		return Token{}, err
	}
	if err := r.checkElementCountLimit(header); err != nil {
		return Token{}, err
	}
	if r.stopBeforePixelData && header.Tag == core.TagPixelData {
		r.elementCount++
		return Token{
			Kind:   TokenElement,
			Header: header,
			Element: core.Element{
				Header: header,
				Value:  nil,
			},
		}, nil
	}
	if err := r.checkElementByteLimit(header); err != nil {
		return Token{}, err
	}
	valueOffset := r.Position()
	if r.frameSink != nil && !r.frameSinkClosed && len(r.seqDelimiters) == 0 && header.Tag == core.TagPixelData && !r.syntax.Encapsulated {
		return r.readNativePixelDataFramesToken(header, valueOffset)
	}
	if (r.skipPixelData || r.deferPixelData) && isPixelDataValueTag(header.Tag) {
		if err := r.skipN(valueOffset, header, int64(header.Length)); err != nil {
			return Token{}, err
		}
		r.recordValueLocation(header, valueOffset, int64(header.Length))
		r.elementCount++
		r.delimiterCheckPending = true
		return Token{
			Kind:   TokenElement,
			Header: header,
			Element: core.Element{
				Header: header,
				Value:  nil,
			},
		}, nil
	}
	if r.deferWaveformData && header.Tag == tagWaveformData {
		if err := r.skipN(valueOffset, header, int64(header.Length)); err != nil {
			return Token{}, err
		}
		r.recordValueLocation(header, valueOffset, int64(header.Length))
		r.elementCount++
		r.delimiterCheckPending = true
		return Token{
			Kind:   TokenElement,
			Header: header,
			Element: core.Element{
				Header: header,
				Value:  nil,
			},
		}, nil
	}
	if r.inlineThreshold > 0 && int64(header.Length) > r.inlineThreshold && header.VR != core.VRUT {
		// Large defined-length primitive.
		//
		// We only support skipping/streaming raw bytes for VRs where consumers can
		// reasonably interpret the bytes without additional parsing/decoding.
		//
		// This includes the typical "byte blob" VRs and native (defined-length)
		// Pixel Data.
		switch header.VR {
		case core.VROB, core.VROW, core.VROF, core.VROD, core.VRUN:
			// ok
		default:
			return Token{}, &ParseError{
				Op:     OpReadValue,
				Offset: valueOffset,
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    fmt.Errorf("dicom: refusing to skip/stream large defined-length value for VR %s", header.VR),
			}
		}
		if err := r.skipN(valueOffset, header, int64(header.Length)); err != nil {
			return Token{}, err
		}
		r.recordValueLocation(header, valueOffset, int64(header.Length))
		r.elementCount++
		r.delimiterCheckPending = true
		return Token{
			Kind:   TokenElement,
			Header: header,
			Element: core.Element{
				Header: header,
				Value:  nil,
			},
		}, nil
	}
	data, err := r.readDefinedValueBytes(header, valueOffset)
	if err != nil {
		return Token{}, err
	}
	if r.encapsulatedSink != nil && len(r.seqDelimiters) == 0 && isExtendedTable(header.Tag) {
		if err := r.encapsulatedSink.ExtendedTable(core.Element{Header: header, Value: core.RawValue(data)}); err != nil {
			return Token{}, err
		}
	}
	if header.VR == core.VRLO {
		if err := r.capturePrivateReservation(core.Element{Header: header, Value: core.RawValue(data)}); err != nil {
			return Token{}, err
		}
	}
	if err := r.captureFrameMetadata(header, data); err != nil {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: valueOffset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    err,
		}
	}
	r.elementCount++
	r.delimiterCheckPending = true
	return Token{
		Kind:   TokenElement,
		Header: header,
		Element: core.Element{
			Header: header,
			Value:  core.RawValue(data),
		},
	}, nil
}

func (r *Reader) readDefinedValueBytes(header core.ElementHeader, valueOffset int64) ([]byte, error) {
	length := int64(header.Length)
	if length == 0 {
		return []byte{}, nil
	}
	if r.slurpEligible {
		r.ensureSlurp()
	}
	maxInt := int64(^uint(0) >> 1)
	if src, ok := r.counter.r.(*sliceSource); ok && length <= maxInt {
		if data, ok := src.take(int(length)); ok {
			r.counter.pos += int64(len(data))
			return data, nil
		}
		// Fewer bytes remain than the declared length; fall through so the
		// streaming paths produce the same truncation errors as any source.
	}
	if length <= definedValueReadChunkSize {
		data := r.allocateDefinedValueBytes(int(length))
		if _, err := io.ReadFull(r.counter, data); err != nil {
			return nil, r.valueReadError(valueOffset, header, err)
		}
		return data, nil
	}
	if length <= maxInt {
		// A configured limit has already validated length in
		// readDefinedValueToken, and a length that fits before the physical end
		// of a sized source cannot be a forged huge VL. Either bound lets the
		// final buffer be allocated exactly once. Unsized streams keep the
		// incremental growth below as their allocation guard.
		if r.maxElementBytes > 0 || (r.sourceEnd > 0 && length <= r.sourceEnd-r.Position()) {
			data := r.allocateDefinedValueBytes(int(length))
			if _, err := io.ReadFull(r.counter, data); err != nil {
				return nil, r.valueReadError(valueOffset, header, err)
			}
			return data, nil
		}
	}

	var out bytes.Buffer
	out.Grow(definedValueReadChunkSize)
	remaining := length
	for remaining > 0 {
		want := int64(definedValueReadChunkSize)
		if remaining < want {
			want = remaining
		}
		out.Grow(int(want))
		// Read into the buffer's free capacity instead of copying through a
		// separate scratch slice. Write only advances the buffer length here.
		dst := out.AvailableBuffer()[:int(want)]
		n, err := io.ReadFull(r.counter, dst)
		if n > 0 {
			out.Write(dst[:n])
			remaining -= int64(n)
		}
		if err != nil {
			return nil, r.valueReadError(valueOffset, header, err)
		}
	}
	return out.Bytes(), nil
}

func (r *Reader) allocateDefinedValueBytes(length int) []byte {
	if length <= 0 {
		return []byte{}
	}
	if length > smallValueArenaLimit {
		return make([]byte, length)
	}
	if cap(r.smallValueArena)-len(r.smallValueArena) < length {
		blockSize := smallValueArenaBlockBytes
		if cap(r.smallValueArena) == 0 {
			blockSize = smallValueArenaInitialBytes
		}
		r.smallValueArena = make([]byte, 0, blockSize)
	}
	start := len(r.smallValueArena)
	r.smallValueArena = r.smallValueArena[:start+length]
	return r.smallValueArena[start : start+length : start+length]
}

func (r *Reader) valueReadError(valueOffset int64, header core.ElementHeader, err error) error {
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	} else {
		err = normalizeReadError(valueOffset, r.Position(), err)
	}
	return &ParseError{
		Op:     OpReadValue,
		Offset: valueOffset,
		Tag:    header.Tag,
		VR:     header.VR,
		Length: header.Length,
		Err:    err,
	}
}

func (r *Reader) readNativePixelDataFramesToken(header core.ElementHeader, valueOffset int64) (Token, error) {
	metadata, err := r.frameMetadata.complete()
	if err != nil {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: valueOffset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err:    err,
		}
	}

	frameSize := metadata.FrameSize()
	totalSize := metadata.TotalSize()
	maxInt := int64(^uint(0) >> 1)
	if frameSize <= 0 || totalSize <= 0 || frameSize > maxInt || totalSize > maxInt {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: valueOffset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err: fmt.Errorf(
				"%w: rows=%d columns=%d samples_per_pixel=%d bits_allocated=%d number_of_frames=%d",
				ErrInvalidFrameMetadata,
				metadata.Rows,
				metadata.Columns,
				metadata.SamplesPerPixel,
				metadata.BitsAllocated,
				metadata.NumberOfFrames,
			),
		}
	}
	encodedSize := int64(header.Length)
	hasPadding := encodedSize == totalSize+1 && totalSize%2 == 1
	if encodedSize != totalSize && !hasPadding {
		return Token{}, &ParseError{
			Op:     OpReadValue,
			Offset: valueOffset,
			Tag:    header.Tag,
			VR:     header.VR,
			Length: header.Length,
			Err: fmt.Errorf(
				"%w: expected %d bytes for %d frame(s), got %d",
				ErrFrameDataSizeMismatch,
				totalSize,
				metadata.NumberOfFrames,
				header.Length,
			),
		}
	}

	frameSizeInt := int(frameSize)
	for i := 0; i < metadata.NumberOfFrames; i++ {
		data := make([]byte, frameSizeInt)
		if _, err := io.ReadFull(r.counter, data); err != nil {
			return Token{}, &ParseError{
				Op:     OpReadValue,
				Offset: valueOffset + int64(i)*frameSize,
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    requiredReadError(err),
			}
		}
		if err := r.frameSink.HandleFrame(Frame{
			Index:          i,
			Data:           data,
			Metadata:       metadata,
			TransferSyntax: r.syntax,
		}); err != nil {
			return Token{}, &ParseError{
				Op:     OpReadValue,
				Offset: r.Position(),
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    fmt.Errorf("%w: frame %d: %w", ErrFrameSink, i, err),
			}
		}
	}
	if hasPadding {
		var pad [1]byte
		padOffset := valueOffset + totalSize
		if _, err := io.ReadFull(r.counter, pad[:]); err != nil {
			return Token{}, &ParseError{
				Op:     OpReadValue,
				Offset: padOffset,
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err:    requiredReadError(err),
			}
		}
		if pad[0] != 0 {
			return Token{}, &ParseError{
				Op:     OpReadValue,
				Offset: padOffset,
				Tag:    header.Tag,
				VR:     header.VR,
				Length: header.Length,
				Err: fmt.Errorf(
					"%w: expected zero padding byte after %d byte payload, got 0x%02X",
					ErrFrameDataSizeMismatch,
					totalSize,
					pad[0],
				),
			}
		}
	}

	r.recordValueLocation(header, valueOffset, int64(header.Length))
	r.elementCount++
	r.delimiterCheckPending = true
	return Token{
		Kind:   TokenElement,
		Header: header,
		Element: core.Element{
			Header: header,
			Value:  nil,
		},
	}, nil
}

func (r *Reader) closeFrameSink(readErr error) error {
	if r == nil || r.frameSink == nil || r.frameSinkClosed {
		return readErr
	}
	r.frameSinkClosed = true
	closeErr := r.frameSink.Close()
	if readErr == nil {
		return closeErr
	}
	if closeErr == nil {
		return readErr
	}
	return errors.Join(readErr, closeErr)
}

func (t seqTokenType) String() string {
	switch t {
	case seqTokenTypeSequence:
		return "sequence"
	case seqTokenTypePixelSequence:
		return "pixel sequence"
	case seqTokenTypeItem:
		return "item"
	default:
		return "unknown"
	}
}

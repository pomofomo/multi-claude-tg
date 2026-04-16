// Package audio provides OGG/Opus decode and encode without ffmpeg.
// Uses github.com/hraban/opus for the Opus codec (CGo, needs libopus).
package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/hraban/opus"
)

// DecodeOGGOpus reads an OGG/Opus file and returns PCM float32 samples
// at the target sample rate (mono). Resamples if the source rate differs.
func DecodeOGGOpus(path string, targetRate int) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Parse OGG pages and extract Opus packets.
	packets, sampleRate, err := demuxOGG(f)
	if err != nil {
		return nil, fmt.Errorf("demux OGG: %w", err)
	}
	if sampleRate == 0 {
		sampleRate = 48000 // Opus default
	}

	dec, err := opus.NewDecoder(sampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("opus decoder: %w", err)
	}

	// Decode all packets to PCM int16, then convert to float32.
	var allSamples []float32
	pcmBuf := make([]int16, 5760) // max Opus frame size at 48kHz

	for _, pkt := range packets {
		n, err := dec.Decode(pkt, pcmBuf)
		if err != nil {
			continue // skip bad packets
		}
		for i := 0; i < n; i++ {
			allSamples = append(allSamples, float32(pcmBuf[i])/32768.0)
		}
	}

	// Resample if needed.
	if sampleRate != targetRate {
		allSamples = Resample(allSamples, sampleRate, targetRate)
	}
	return allSamples, nil
}

// EncodeOGGOpus encodes PCM float32 samples to an OGG/Opus file.
func EncodeOGGOpus(samples []float32, sampleRate int, outPath string) error {
	enc, err := opus.NewEncoder(sampleRate, 1, opus.AppVoIP)
	if err != nil {
		return fmt.Errorf("opus encoder: %w", err)
	}
	if err := enc.SetBitrate(64000); err != nil {
		return err
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Frame size: 20ms of audio.
	frameSize := sampleRate * 20 / 1000
	serialNo := uint32(1)

	// Write OGG ID header page.
	idHeader := makeOpusIDHeader(1, sampleRate)
	if err := writeOGGPage(f, serialNo, 0, 0, 2, idHeader); err != nil {
		return err
	}

	// Write OGG comment header page.
	commentHeader := makeOpusCommentHeader()
	if err := writeOGGPage(f, serialNo, 1, 0, 0, commentHeader); err != nil {
		return err
	}

	// Encode audio frames.
	pageSeq := uint32(2)
	granulePos := uint64(0)
	opusBuf := make([]byte, 4000) // max encoded frame

	for offset := 0; offset < len(samples); offset += frameSize {
		end := offset + frameSize
		if end > len(samples) {
			// Pad last frame with silence.
			frame := make([]float32, frameSize)
			copy(frame, samples[offset:])
			n, err := enc.EncodeFloat32(frame, opusBuf)
			if err != nil {
				return fmt.Errorf("opus encode: %w", err)
			}
			granulePos += uint64(frameSize)
			flags := byte(4) // EOS
			if err := writeOGGPage(f, serialNo, pageSeq, granulePos, flags, opusBuf[:n]); err != nil {
				return err
			}
			break
		}

		n, err := enc.EncodeFloat32(samples[offset:end], opusBuf)
		if err != nil {
			return fmt.Errorf("opus encode: %w", err)
		}
		granulePos += uint64(frameSize)
		if err := writeOGGPage(f, serialNo, pageSeq, granulePos, 0, opusBuf[:n]); err != nil {
			return err
		}
		pageSeq++
	}
	return nil
}

// --- OGG demuxer (minimal, Opus-only) ---

func demuxOGG(r io.Reader) (packets [][]byte, sampleRate int, err error) {
	sampleRate = 48000
	headersParsed := 0

	for {
		// Read OGG page header.
		var hdr [27]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				break
			}
			return nil, 0, err
		}
		if string(hdr[0:4]) != "OggS" {
			return nil, 0, fmt.Errorf("not an OGG file")
		}

		nSegments := int(hdr[26])
		segTable := make([]byte, nSegments)
		if _, err := io.ReadFull(r, segTable); err != nil {
			return nil, 0, err
		}

		// Read page data.
		totalSize := 0
		for _, s := range segTable {
			totalSize += int(s)
		}
		pageData := make([]byte, totalSize)
		if _, err := io.ReadFull(r, pageData); err != nil {
			return nil, 0, err
		}

		// Split into packets (segments of 255 are continuations).
		offset := 0
		var pkt []byte
		for _, s := range segTable {
			pkt = append(pkt, pageData[offset:offset+int(s)]...)
			offset += int(s)
			if s < 255 {
				if headersParsed < 2 {
					// Parse OpusHead for sample rate.
					if headersParsed == 0 && len(pkt) >= 12 && string(pkt[0:8]) == "OpusHead" {
						sampleRate = int(binary.LittleEndian.Uint32(pkt[12:16]))
						if sampleRate == 0 {
							sampleRate = 48000
						}
					}
					headersParsed++
				} else {
					packets = append(packets, pkt)
				}
				pkt = nil
			}
		}
	}
	return packets, sampleRate, nil
}

// --- OGG muxer (minimal, Opus-only) ---

func makeOpusIDHeader(channels int, sampleRate int) []byte {
	// https://tools.ietf.org/html/rfc7845#section-5.1
	buf := make([]byte, 19)
	copy(buf[0:8], "OpusHead")
	buf[8] = 1 // version
	buf[9] = byte(channels)
	binary.LittleEndian.PutUint16(buf[10:12], 0) // pre-skip
	binary.LittleEndian.PutUint32(buf[12:16], uint32(sampleRate))
	binary.LittleEndian.PutUint16(buf[16:18], 0) // output gain
	buf[18] = 0 // channel mapping family
	return buf
}

func makeOpusCommentHeader() []byte {
	var buf bytes.Buffer
	buf.WriteString("OpusTags")
	vendor := "trd"
	binary.Write(&buf, binary.LittleEndian, uint32(len(vendor)))
	buf.WriteString(vendor)
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // no user comments
	return buf.Bytes()
}

func writeOGGPage(w io.Writer, serialNo uint32, pageSeq uint32, granulePos uint64, flags byte, data []byte) error {
	// Segment table: split data into 255-byte segments.
	nFullSegs := len(data) / 255
	lastSeg := len(data) % 255
	nSegs := nFullSegs
	if lastSeg > 0 || len(data) == 0 {
		nSegs++
	}

	// Page header.
	var hdr bytes.Buffer
	hdr.WriteString("OggS")
	hdr.WriteByte(0) // version
	hdr.WriteByte(flags)
	binary.Write(&hdr, binary.LittleEndian, granulePos)
	binary.Write(&hdr, binary.LittleEndian, serialNo)
	binary.Write(&hdr, binary.LittleEndian, pageSeq)
	binary.Write(&hdr, binary.LittleEndian, uint32(0)) // CRC placeholder
	hdr.WriteByte(byte(nSegs))

	// Segment table.
	for i := 0; i < nFullSegs; i++ {
		hdr.WriteByte(255)
	}
	if lastSeg > 0 || len(data) == 0 {
		hdr.WriteByte(byte(lastSeg))
	}

	// Compute CRC over header + data.
	page := append(hdr.Bytes(), data...)
	crc := oggCRC(page)
	binary.LittleEndian.PutUint32(page[22:26], crc)

	_, err := w.Write(page)
	return err
}

// oggCRC computes the OGG page CRC32 (polynomial 0x04C11DB7).
func oggCRC(data []byte) uint32 {
	var crc uint32
	for _, b := range data {
		crc = (crc << 8) ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}

var oggCRCTable = func() [256]uint32 {
	var t [256]uint32
	for i := 0; i < 256; i++ {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ 0x04C11DB7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

// --- Resampler ---

// Resample does simple linear interpolation resampling.
func Resample(samples []float32, fromRate, toRate int) []float32 {
	if fromRate == toRate {
		return samples
	}
	ratio := float64(fromRate) / float64(toRate)
	outLen := int(math.Ceil(float64(len(samples)) / ratio))
	out := make([]float32, outLen)
	for i := range out {
		srcPos := float64(i) * ratio
		idx := int(srcPos)
		frac := float32(srcPos - float64(idx))
		if idx+1 < len(samples) {
			out[i] = samples[idx]*(1-frac) + samples[idx+1]*frac
		} else if idx < len(samples) {
			out[i] = samples[idx]
		}
	}
	return out
}

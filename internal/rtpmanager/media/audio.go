package media

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/zaf/g711"
)

// WAVHeader represents a WAV file header
type WAVHeader struct {
	ChunkID       [4]byte // "RIFF"
	ChunkSize     uint32
	Format        [4]byte // "WAVE"
	Subchunk1ID   [4]byte // "fmt "
	Subchunk1Size uint32
	AudioFormat   uint16
	NumChannels   uint16
	SampleRate    uint32
	ByteRate      uint32
	BlockAlign    uint16
	BitsPerSample uint16
}

// AudioFile represents parsed audio file metadata and data
type AudioFile struct {
	AudioFormat   uint16
	SampleRate    uint32
	NumChannels   uint16
	BitsPerSample uint16
	PCMData       []byte
}

// ParseWAVData parses WAV data from a byte slice and returns metadata + PCM audio data
func ParseWAVData(data []byte) (*AudioFile, error) {
	if len(data) < 44 {
		return nil, fmt.Errorf("WAV data too short")
	}

	// Read RIFF header
	if string(data[0:4]) != "RIFF" {
		return nil, fmt.Errorf("not a valid RIFF file")
	}

	// Read WAVE header
	if string(data[8:12]) != "WAVE" {
		return nil, fmt.Errorf("not a valid WAVE file")
	}

	// Parse chunks
	audioFile := &AudioFile{}
	offset := 12

	for offset < len(data)-8 {
		chunkID := string(data[offset : offset+4])
		chunkSize := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		offset += 8

		switch chunkID {
		case "fmt ":
			if offset+16 > len(data) {
				return nil, fmt.Errorf("fmt chunk too short")
			}
			audioFile.AudioFormat = binary.LittleEndian.Uint16(data[offset : offset+2])
			if audioFile.AudioFormat != 1 {
				return nil, fmt.Errorf("only PCM audio format (1) is supported, got %d", audioFile.AudioFormat)
			}
			audioFile.NumChannels = binary.LittleEndian.Uint16(data[offset+2 : offset+4])
			audioFile.SampleRate = binary.LittleEndian.Uint32(data[offset+4 : offset+8])
			audioFile.BitsPerSample = binary.LittleEndian.Uint16(data[offset+14 : offset+16])
			slog.Debug("[WAV] Parsed format chunk", "sampleRate", audioFile.SampleRate, "channels", audioFile.NumChannels, "bitsPerSample", audioFile.BitsPerSample)
			offset += int(chunkSize)

		case "data":
			endOffset := offset + int(chunkSize)
			if endOffset > len(data) {
				endOffset = len(data)
			}
			audioFile.PCMData = data[offset:endOffset]
			slog.Debug("[WAV] Loaded audio data", "size_bytes", len(audioFile.PCMData))
			return audioFile, nil

		default:
			offset += int(chunkSize)
		}
	}

	return nil, fmt.Errorf("data chunk not found in WAV data")
}

// ReadWAVFile parses a WAV file and returns metadata + PCM audio data
func ReadWAVFile(filePath string) (*AudioFile, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Read RIFF header
	riffID := make([]byte, 4)
	if _, err := file.Read(riffID); err != nil {
		return nil, fmt.Errorf("failed to read RIFF header: %w", err)
	}
	if string(riffID) != "RIFF" {
		return nil, fmt.Errorf("not a valid RIFF file")
	}

	// Read RIFF size
	var riffSize uint32
	if err := binary.Read(file, binary.LittleEndian, &riffSize); err != nil {
		return nil, fmt.Errorf("failed to read RIFF size: %w", err)
	}

	// Read WAVE header
	waveID := make([]byte, 4)
	if _, err := file.Read(waveID); err != nil {
		return nil, fmt.Errorf("failed to read WAVE header: %w", err)
	}
	if string(waveID) != "WAVE" {
		return nil, fmt.Errorf("not a valid WAVE file")
	}

	// Find and parse fmt chunk
	audioFile := &AudioFile{}
	for {
		chunkID := make([]byte, 4)
		n, err := file.Read(chunkID)
		if n == 0 || err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read chunk ID: %w", err)
		}

		var chunkSize uint32
		if err := binary.Read(file, binary.LittleEndian, &chunkSize); err != nil {
			return nil, fmt.Errorf("failed to read chunk size: %w", err)
		}

		switch string(chunkID) {
		case "fmt ":
			// Parse format chunk
			if err := binary.Read(file, binary.LittleEndian, &audioFile.AudioFormat); err != nil {
				return nil, fmt.Errorf("failed to read audio format: %w", err)
			}
			if audioFile.AudioFormat != 1 {
				return nil, fmt.Errorf("only PCM audio format (1) is supported, got %d", audioFile.AudioFormat)
			}

			if err := binary.Read(file, binary.LittleEndian, &audioFile.NumChannels); err != nil {
				return nil, fmt.Errorf("failed to read channels: %w", err)
			}
			if err := binary.Read(file, binary.LittleEndian, &audioFile.SampleRate); err != nil {
				return nil, fmt.Errorf("failed to read sample rate: %w", err)
			}

			// Skip byte rate and block align
			if _, err := file.Seek(6, 1); err != nil {
				return nil, fmt.Errorf("failed to seek past byte rate: %w", err)
			}

			if err := binary.Read(file, binary.LittleEndian, &audioFile.BitsPerSample); err != nil {
				return nil, fmt.Errorf("failed to read bits per sample: %w", err)
			}

			slog.Info("[WAV] Parsed format chunk", "sampleRate", audioFile.SampleRate, "channels", audioFile.NumChannels, "bitsPerSample", audioFile.BitsPerSample)

		case "data":
			// Read audio data
			audioData := make([]byte, chunkSize)
			if _, err := file.Read(audioData); err != nil {
				return nil, fmt.Errorf("failed to read audio data: %w", err)
			}
			audioFile.PCMData = audioData
			slog.Info("[WAV] Loaded audio data", "file", filePath, "size_bytes", len(audioData))
			return audioFile, nil

		default:
			// Skip unknown chunks
			if _, err := file.Seek(int64(chunkSize), 1); err != nil {
				return nil, fmt.Errorf("failed to skip chunk: %w", err)
			}
		}
	}

	return nil, fmt.Errorf("data chunk not found in WAV file")
}

// ResampleAudio converts audio to 8000 Hz mono 16-bit PCM
func ResampleAudio(audioFile *AudioFile) ([]byte, error) {
	const targetSampleRate = 8000

	// Convert to mono if needed
	var monoPCM []byte
	if audioFile.NumChannels == 1 {
		monoPCM = audioFile.PCMData
	} else if audioFile.NumChannels == 2 {
		// Simple stereo to mono conversion (average channels)
		monoPCM = make([]byte, len(audioFile.PCMData)/2)
		for i := 0; i < len(audioFile.PCMData); i += 4 {
			// Read left and right samples (16-bit little-endian)
			left := int16(audioFile.PCMData[i]) | int16(audioFile.PCMData[i+1])<<8
			right := int16(audioFile.PCMData[i+2]) | int16(audioFile.PCMData[i+3])<<8
			mono := (int32(left) + int32(right)) / 2
			// Write mono sample (16-bit little-endian)
			monoPCM[i/2] = byte(mono & 0xFF)
			monoPCM[i/2+1] = byte((mono >> 8) & 0xFF)
		}
	} else {
		return nil, fmt.Errorf("unsupported number of channels: %d", audioFile.NumChannels)
	}

	// Resample if needed
	if audioFile.SampleRate == targetSampleRate {
		return monoPCM, nil
	}

	slog.Info("[AUDIO] Resampling", "from", audioFile.SampleRate, "to", targetSampleRate, "inputSize", len(monoPCM))

	// Linear interpolation resampling
	ratio := float64(audioFile.SampleRate) / float64(targetSampleRate)
	outputSamples := int(float64(len(monoPCM)/2) / ratio)
	outputPCM := make([]byte, outputSamples*2)

	for i := 0; i < outputSamples; i++ {
		srcPos := float64(i) * ratio
		srcIdx := int(srcPos)
		frac := srcPos - float64(srcIdx)

		if srcIdx+2 >= len(monoPCM)/2 {
			// Out of bounds, stop
			outputPCM = outputPCM[:i*2]
			break
		}

		// Read two consecutive samples for interpolation
		sample1 := int16(monoPCM[srcIdx*2]) | int16(monoPCM[srcIdx*2+1])<<8
		sample2 := int16(monoPCM[(srcIdx+1)*2]) | int16(monoPCM[(srcIdx+1)*2+1])<<8

		// Linear interpolation
		interpolated := int16(float64(sample1)*(1-frac) + float64(sample2)*frac)

		// Write resampled sample (16-bit little-endian)
		outputPCM[i*2] = byte(interpolated & 0xFF)
		outputPCM[i*2+1] = byte((interpolated >> 8) & 0xFF)
	}

	slog.Info("[AUDIO] Resampling complete", "outputSize", len(outputPCM))
	return outputPCM, nil
}

// PCMToPCMU converts 16-bit PCM samples to PCMU (µ-law) encoding using g711 library
func PCMToPCMU(pcm []byte) []byte {
	// Use the battle-tested g711 library which handles the conversion properly
	return g711.EncodeUlaw(pcm)
}

// PCMUToPCM converts PCMU (µ-law) encoded bytes to 16-bit PCM samples
func PCMUToPCM(ulaw []byte) []byte {
	return g711.DecodeUlaw(ulaw)
}

// Upsample8to16 converts 8kHz 16-bit PCM to 16kHz 16-bit PCM using linear interpolation
func Upsample8to16(pcm8k []byte) []byte {
	// Each sample is 2 bytes (16-bit), output will be 2x the samples
	numSamples := len(pcm8k) / 2
	if numSamples == 0 {
		return nil
	}

	// Output: 2x samples (each original sample becomes 2)
	output := make([]byte, numSamples*4)

	for i := 0; i < numSamples; i++ {
		// Read current sample (16-bit little-endian)
		sample := int16(pcm8k[i*2]) | int16(pcm8k[i*2+1])<<8

		// Get next sample for interpolation (or use current for last sample)
		var nextSample int16
		if i+1 < numSamples {
			nextSample = int16(pcm8k[(i+1)*2]) | int16(pcm8k[(i+1)*2+1])<<8
		} else {
			nextSample = sample
		}

		// Write original sample
		output[i*4] = byte(sample & 0xFF)
		output[i*4+1] = byte((sample >> 8) & 0xFF)

		// Write interpolated sample (midpoint)
		interpolated := int16((int32(sample) + int32(nextSample)) / 2)
		output[i*4+2] = byte(interpolated & 0xFF)
		output[i*4+3] = byte((interpolated >> 8) & 0xFF)
	}

	return output
}

// CalculateEnergy calculates the RMS energy of 16-bit PCM samples
// Returns a value that can be compared to a threshold for silence detection
func CalculateEnergy(pcm []byte) float64 {
	if len(pcm) < 2 {
		return 0
	}

	var sum int64
	numSamples := len(pcm) / 2

	for i := 0; i < numSamples; i++ {
		sample := int16(pcm[i*2]) | int16(pcm[i*2+1])<<8
		sum += int64(sample) * int64(sample)
	}

	// Return RMS energy
	return float64(sum) / float64(numSamples)
}

// BuildWAVHeader creates a WAV file header for the given audio parameters
func BuildWAVHeader(dataSize int, sampleRate uint32, numChannels uint16, bitsPerSample uint16) []byte {
	byteRate := sampleRate * uint32(numChannels) * uint32(bitsPerSample) / 8
	blockAlign := numChannels * bitsPerSample / 8

	header := make([]byte, 44)

	// RIFF header
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+dataSize))
	copy(header[8:12], "WAVE")

	// fmt subchunk
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16) // Subchunk1Size for PCM
	binary.LittleEndian.PutUint16(header[20:22], 1)  // AudioFormat (1 = PCM)
	binary.LittleEndian.PutUint16(header[22:24], numChannels)
	binary.LittleEndian.PutUint32(header[24:28], sampleRate)
	binary.LittleEndian.PutUint32(header[28:32], byteRate)
	binary.LittleEndian.PutUint16(header[32:34], blockAlign)
	binary.LittleEndian.PutUint16(header[34:36], bitsPerSample)

	// data subchunk
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(dataSize))

	return header
}

// BuildWAVData creates a complete WAV file from 16-bit PCM data
func BuildWAVData(pcmData []byte, sampleRate uint32) []byte {
	header := BuildWAVHeader(len(pcmData), sampleRate, 1, 16)
	wav := make([]byte, len(header)+len(pcmData))
	copy(wav, header)
	copy(wav[len(header):], pcmData)
	return wav
}

// --- Inventory ---

// WAVInfo is a WAV file's header, without its audio.
//
// Listing a directory must not load every PCM payload into memory to answer
// "what format is this", which is what ReadWAVFile would do.
type WAVInfo struct {
	AudioFormat   uint16
	NumChannels   uint16
	BitsPerSample uint16
	SampleRate    uint32
	DataBytes     int64
}

// DurationMs estimates the playing time from the header alone.
func (i WAVInfo) DurationMs() int64 {
	bytesPerFrame := int64(i.NumChannels) * int64(i.BitsPerSample) / 8
	if bytesPerFrame <= 0 || i.SampleRate == 0 {
		return 0
	}
	return i.DataBytes * 1000 / (bytesPerFrame * int64(i.SampleRate))
}

// Problem reports why the player would refuse this file, or why it will not
// sound as recorded. An empty string means the file is exactly what the player
// wants; playable is false only for the refusals.
//
// The rules are read off what the player actually does, not off a general idea
// of what a WAV is: ParseWAVData rejects anything but PCM, ResampleAudio assumes
// 16-bit little-endian samples and handles at most two channels, and anything
// not already 8 kHz mono is converted on every single call.
func (i WAVInfo) Problem() (problem string, playable bool) {
	switch {
	case i.AudioFormat != 1:
		return fmt.Sprintf(
			"encoded as format %d, not uncompressed PCM; the player refuses it. Convert it — the "+
				"µ-law conversion happens on the way out, so the file itself must be PCM", i.AudioFormat), false
	case i.BitsPerSample != 16:
		return fmt.Sprintf(
			"%d-bit samples; the player assumes 16-bit and would emit noise", i.BitsPerSample), false
	case i.NumChannels == 0 || i.NumChannels > 2:
		return fmt.Sprintf("%d channels; the player handles mono or stereo only", i.NumChannels), false
	case i.DataBytes == 0:
		return "no audio data", false
	case i.SampleRate != 8000 && i.NumChannels == 2:
		return fmt.Sprintf(
			"%d Hz stereo; it plays, but is downmixed and resampled to 8 kHz mono on every call",
			i.SampleRate), true
	case i.SampleRate != 8000:
		return fmt.Sprintf("%d Hz; it plays, but is resampled to 8 kHz on every call", i.SampleRate), true
	case i.NumChannels == 2:
		return "stereo; it plays, but is downmixed to mono on every call", true
	}
	return "", true
}

// ProbeWAVFile reads a WAV file's header without reading its audio.
func ProbeWAVFile(filePath string) (WAVInfo, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return WAVInfo{}, err
	}
	defer f.Close()

	// Enough for the RIFF/WAVE header plus a generous run of chunk headers
	// before the data chunk. A file whose fmt chunk is further in than this is
	// not one the player would handle either.
	buf := make([]byte, 4096)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return WAVInfo{}, err
	}
	buf = buf[:n]

	if len(buf) < 12 || string(buf[0:4]) != "RIFF" || string(buf[8:12]) != "WAVE" {
		return WAVInfo{}, fmt.Errorf("not a RIFF/WAVE file")
	}

	var info WAVInfo
	sawFmt := false
	offset := 12
	for offset+8 <= len(buf) {
		chunkID := string(buf[offset : offset+4])
		chunkSize := binary.LittleEndian.Uint32(buf[offset+4 : offset+8])
		offset += 8

		switch chunkID {
		case "fmt ":
			if offset+16 > len(buf) {
				return WAVInfo{}, fmt.Errorf("truncated fmt chunk")
			}
			info.AudioFormat = binary.LittleEndian.Uint16(buf[offset : offset+2])
			info.NumChannels = binary.LittleEndian.Uint16(buf[offset+2 : offset+4])
			info.SampleRate = binary.LittleEndian.Uint32(buf[offset+4 : offset+8])
			info.BitsPerSample = binary.LittleEndian.Uint16(buf[offset+14 : offset+16])
			sawFmt = true
			offset += int(chunkSize)

		case "data":
			// The declared size, not what was read: the payload is deliberately
			// not loaded.
			info.DataBytes = int64(chunkSize)
			if !sawFmt {
				return WAVInfo{}, fmt.Errorf("data chunk before fmt chunk")
			}
			return info, nil

		default:
			offset += int(chunkSize)
		}
	}

	if !sawFmt {
		return WAVInfo{}, fmt.Errorf("fmt chunk not found")
	}
	return info, fmt.Errorf("data chunk not found")
}

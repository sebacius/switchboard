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

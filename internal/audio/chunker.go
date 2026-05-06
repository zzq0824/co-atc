package audio

import (
	"bytes"
	"fmt"
)

// AudioChunker 处理音频数据的分块
type AudioChunker struct {
	sampleRate  int
	channels    int
	chunkSizeMs int
	buffer      *bytes.Buffer
	bytesPerMs  int
}

// NewAudioChunker 创建一个新的音频分块器
func NewAudioChunker(sampleRate, channels, chunkSizeMs int) *AudioChunker {
	// 计算每毫秒的字节数
	// 对于 PCM16,每个采样为 2 字节(16 位)
	bytesPerSample := 2
	bytesPerMs := (sampleRate * channels * bytesPerSample) / 1000

	return &AudioChunker{
		sampleRate:  sampleRate,
		channels:    channels,
		chunkSizeMs: chunkSizeMs,
		buffer:      bytes.NewBuffer(nil),
		bytesPerMs:  bytesPerMs,
	}
}

// ProcessChunk 处理一个音频分块并返回 base64 编码的分块
func (c *AudioChunker) ProcessChunk(data []byte) ([][]byte, error) {
	// 将数据添加到缓冲区
	if _, err := c.buffer.Write(data); err != nil {
		return nil, fmt.Errorf("写入缓冲区失败: %w", err)
	}

	// 计算分块大小(字节数)
	chunkSizeBytes := c.chunkSizeMs * c.bytesPerMs

	// 提取分块
	var chunks [][]byte
	for c.buffer.Len() >= chunkSizeBytes {
		chunk := make([]byte, chunkSizeBytes)
		n, err := c.buffer.Read(chunk)
		if err != nil {
			return nil, fmt.Errorf("从缓冲区读取失败: %w", err)
		}
		if n < chunkSizeBytes {
			// 这种情况不应发生,但以防万一
			chunk = chunk[:n]
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// Reset 重置缓冲区
func (c *AudioChunker) Reset() {
	c.buffer.Reset()
}

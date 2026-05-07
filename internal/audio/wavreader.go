package audio

import (
	"encoding/binary"
	"io"
)

// WAVHeader 表示 WAV 文件头
type WAVHeader struct {
	// RIFF chunk descriptor
	ChunkID   [4]byte // "RIFF"
	ChunkSize uint32  // 4 + (8 + SubChunk1Size) + (8 + SubChunk2Size)
	Format    [4]byte // "WAVE"

	// "fmt " 子块
	Subchunk1ID   [4]byte // "fmt "
	Subchunk1Size uint32  // PCM 为 16
	AudioFormat   uint16  // PCM 为 1
	NumChannels   uint16  // 单声道为 1,立体声为 2
	SampleRate    uint32  // 8000、44100 等
	ByteRate      uint32  // SampleRate * NumChannels * BitsPerSample/8
	BlockAlign    uint16  // NumChannels * BitsPerSample/8
	BitsPerSample uint16  // 8、16 等

	// "data" 子块
	Subchunk2ID   [4]byte // "data"
	Subchunk2Size uint32  // NumSamples * NumChannels * BitsPerSample/8
}

// WAVReader 包装一个读取器并在前面附加 WAV 头
type WAVReader struct {
	reader     io.ReadCloser
	headerSent bool
	header     []byte
}

// NewWAVReader 创建一个新的 WAV 读取器
func NewWAVReader(reader io.ReadCloser, sampleRate, channels int) *WAVReader {
	// 创建 WAV 头
	header := createWAVHeader(sampleRate, channels)

	return &WAVReader{
		reader:     reader,
		headerSent: false,
		header:     header,
	}
}

// createWAVHeader 使用指定参数创建 WAV 头
func createWAVHeader(sampleRate, channels int) []byte {
	bitsPerSample := uint16(16) // 16 位 PCM

	// 计算派生值
	byteRate := uint32(sampleRate * channels * int(bitsPerSample/8))
	blockAlign := uint16(channels * int(bitsPerSample/8))

	// 我们不知道实际的数据大小,因此使用一个非常大的值
	// 这对流式音频来说没有问题
	dataSize := uint32(0xFFFFFFFF - 36) // 最大尺寸减去头部尺寸

	// 创建头部结构
	header := WAVHeader{
		ChunkID:   [4]byte{'R', 'I', 'F', 'F'},
		ChunkSize: 36 + dataSize, // 4 + (8 + 16) + (8 + dataSize)
		Format:    [4]byte{'W', 'A', 'V', 'E'},

		Subchunk1ID:   [4]byte{'f', 'm', 't', ' '},
		Subchunk1Size: 16, // PCM 为 16
		AudioFormat:   1,  // PCM 为 1
		NumChannels:   uint16(channels),
		SampleRate:    uint32(sampleRate),
		ByteRate:      byteRate,
		BlockAlign:    blockAlign,
		BitsPerSample: bitsPerSample,

		Subchunk2ID:   [4]byte{'d', 'a', 't', 'a'},
		Subchunk2Size: dataSize,
	}

	// 将头部转换为字节
	headerBytes := make([]byte, 44)

	// RIFF chunk descriptor
	copy(headerBytes[0:4], header.ChunkID[:])
	binary.LittleEndian.PutUint32(headerBytes[4:8], header.ChunkSize)
	copy(headerBytes[8:12], header.Format[:])

	// "fmt " 子块
	copy(headerBytes[12:16], header.Subchunk1ID[:])
	binary.LittleEndian.PutUint32(headerBytes[16:20], header.Subchunk1Size)
	binary.LittleEndian.PutUint16(headerBytes[20:22], header.AudioFormat)
	binary.LittleEndian.PutUint16(headerBytes[22:24], header.NumChannels)
	binary.LittleEndian.PutUint32(headerBytes[24:28], header.SampleRate)
	binary.LittleEndian.PutUint32(headerBytes[28:32], header.ByteRate)
	binary.LittleEndian.PutUint16(headerBytes[32:34], header.BlockAlign)
	binary.LittleEndian.PutUint16(headerBytes[34:36], header.BitsPerSample)

	// "data" 子块
	copy(headerBytes[36:40], header.Subchunk2ID[:])
	binary.LittleEndian.PutUint32(headerBytes[40:44], header.Subchunk2Size)

	return headerBytes
}

// Read 从读取器读取数据,首次读取时在前面附加 WAV 头
func (wr *WAVReader) Read(p []byte) (n int, err error) {
	// 如果头部还未发送,先发送头部
	if !wr.headerSent {
		headerLen := len(wr.header)

		// 如果缓冲区太小无法容纳头部,返回错误
		if len(p) < headerLen {
			return 0, io.ErrShortBuffer
		}

		// 将头部复制到缓冲区
		copy(p, wr.header)
		wr.headerSent = true

		// 如果缓冲区还有空间容纳更多数据,从底层读取器读取
		if len(p) > headerLen {
			m, err := wr.reader.Read(p[headerLen:])
			return headerLen + m, err
		}

		return headerLen, nil
	}

	// 头部已发送,只需从底层读取器读取
	return wr.reader.Read(p)
}

// Close 关闭底层读取器
func (wr *WAVReader) Close() error {
	return wr.reader.Close()
}

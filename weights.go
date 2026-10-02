package needle

import (
	"encoding/binary"
	"math"
	"slices"
)

const (
	modelText   = 1
	modelSpeech = 2
)

func manifestPosition(blob []byte) (directory, count, index uint64, ok bool) {
	const headerSize, recordSize = 196, 44
	if len(blob) < headerSize || binary.LittleEndian.Uint32(blob) != needle3WeightsTag {
		return
	}
	field := func(i int) uint64 { return uint64(binary.LittleEndian.Uint32(blob[i*4:])) }
	count, directory = field(1), headerSize+field(2)*4
	if directory > uint64(len(blob)) || count > (uint64(len(blob))-directory)/recordSize || field(7) == 0 || field(10) == 0 || field(31) > 16 {
		return
	}
	perLayer := uint64(24)
	if field(19) != 0 {
		perLayer += 3
	}
	// Embedding, blocks, mHC, permutations, engrams and final_norm precede
	// either the probe-head manifest or the speech manifest.
	index = 13 + field(10)*perLayer + field(31)*4
	ok = index < count
	return
}

// weightsKind distinguishes text and speech before needle_load can replace
// either process-global model. The shared format tag does not identify the kind.
func weightsKind(blob []byte) int {
	const recordSize = 44
	directory, count, index, ok := manifestPosition(blob)
	if !ok {
		return 0
	}
	record := blob[directory+index*recordSize : directory+(index+1)*recordSize]
	if binary.LittleEndian.Uint16(record[2:]) != 0 || binary.LittleEndian.Uint32(record[36:]) != 0 || binary.LittleEndian.Uint32(record[40:]) != 0 {
		return 0
	}
	offset, size := binary.LittleEndian.Uint64(record[20:]), binary.LittleEndian.Uint64(record[28:])
	if offset < directory+count*recordSize || offset > uint64(len(blob)) || size == 0 || size > uint64(len(blob))-offset {
		return 0
	}
	shape := binary.LittleEndian.Uint32(record[4:])
	for i := 1; i < 4; i++ {
		if binary.LittleEndian.Uint32(record[4+i*4:]) != 0 {
			return 0
		}
	}
	data := blob[offset : offset+size]
	if record[0] == 4 && record[1] == 0 && shape == 0 && index+1 == count {
		return modelText // Text archive without probe heads.
	}
	if record[0] == 1 && record[1] == 1 && shape > 0 && shape <= 3 && size == uint64(shape)*2 {
		previous := uint16(0)
		for i := range shape {
			code := binary.LittleEndian.Uint16(data[i*2:])
			if code <= previous || (code != 0x3c00 && code != 0x4000 && code != 0x4200) {
				return 0
			}
			previous = code
		}
		return modelText
	}
	// ponytail: recognize the published version-1 audio manifest only; extend
	// this classifier when upstream publishes another speech archive layout.
	if record[0] == 2 && record[1] == 1 && shape == 13 && size == 52 && index+1 < count {
		value := func(i int) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:])) }
		if value(0) == 3246 && value(1) == 1 && value(9) == SpeechSampleRate {
			return modelSpeech
		}
	}
	return 0
}

// confidenceHeadPresent inspects only the documented v3 head metadata. The
// native loader validates the model itself; unknown head layouts fail closed.
func confidenceHeadPresent(blob []byte) bool {
	const recordSize = 44
	directory, count, index, ok := manifestPosition(blob)
	if !ok || index+2 > count {
		return false
	}
	field := func(index int) uint64 { return uint64(binary.LittleEndian.Uint32(blob[index*4:])) }
	width, layers := field(7), field(10)
	dataStart := directory + count*recordSize
	previousEnd := dataStart
	read := func(index uint64) (cactTensor, bool) {
		if index >= count {
			return cactTensor{}, false
		}
		record := blob[directory+index*recordSize : directory+(index+1)*recordSize]
		tensor := cactTensor{dtype: record[0]}
		ndim := int(record[1])
		if ndim > 4 || binary.LittleEndian.Uint16(record[2:]) != 0 {
			return tensor, false
		}
		for i := range 4 {
			size := uint64(binary.LittleEndian.Uint32(record[4+i*4:]))
			if i < ndim {
				if size == 0 {
					return tensor, false
				}
				tensor.shape = append(tensor.shape, size)
			} else if size != 0 {
				return tensor, false
			}
		}
		offset, size := binary.LittleEndian.Uint64(record[20:]), binary.LittleEndian.Uint64(record[28:])
		if offset < previousEnd || offset > uint64(len(blob)) || size > uint64(len(blob))-offset || size == 0 {
			return tensor, false
		}
		group, bits := uint64(binary.LittleEndian.Uint32(record[36:])), binary.LittleEndian.Uint32(record[40:])
		switch tensor.dtype {
		case 1: // FP16
			elements := uint64(1)
			for _, dimension := range tensor.shape {
				if dimension > size/2/elements {
					return tensor, false
				}
				elements *= dimension
			}
			if size != elements*2 || group != 0 || bits != 0 {
				return tensor, false
			}
		case 3: // Current probe matrices are CQ4, with padded input groups.
			if ndim != 2 || bits != 4 || group < 2 || group&(group-1) != 0 {
				return tensor, false
			}
			groups := (tensor.shape[1] + group - 1) / group
			bytesPerRow := groups * (group/2 + 2)
			if tensor.shape[0] > size/bytesPerRow || size != tensor.shape[0]*bytesPerRow {
				return tensor, false
			}
		case 4: // RAW tokenizer
			if ndim != 0 || group != 0 || bits != 0 {
				return tensor, false
			}
		default:
			return tensor, false
		}
		tensor.data = blob[offset : offset+size]
		previousEnd = offset + size
		return tensor, true
	}
	finalNorm, ok := read(index - 1)
	if !ok || !finalNorm.matches(1, width) {
		return false
	}
	manifest, ok := read(index)
	if !ok || manifest.dtype != 1 || len(manifest.shape) != 1 || manifest.shape[0] > 3 {
		return false
	}
	index++
	hasConfidence, previousCode := false, uint16(0)
	for i := uint64(0); i < manifest.shape[0]; i++ {
		code := binary.LittleEndian.Uint16(manifest.data[i*2:])
		if code <= previousCode || (code != 0x3c00 && code != 0x4000 && code != 0x4200) {
			return false
		}
		previousCode = code
		hasConfidence = hasConfidence || code == 0x4000
		var head [6]cactTensor
		for j := range head {
			head[j], ok = read(index)
			if !ok {
				return false
			}
			index++
		}
		if head[1].dtype != 1 || len(head[1].shape) != 2 || len(head[2].shape) != 2 || len(head[5].shape) != 1 {
			return false
		}
		k, q, output := head[1].shape[1], head[2].shape[0], head[5].shape[0]
		matrixType := head[0].dtype
		probes := head[0].matches(3, (layers+1)*k, width) || head[0].matches(1, layers+1, k, width)
		if !probes || !head[1].matches(1, layers+1, k) || !head[2].matches(matrixType, q, width) ||
			!head[3].matches(1, q, layers+1, k) || !head[4].matches(matrixType, output, q*width) || !head[5].matches(1, output) ||
			(code == 0x4000 && output != 1) || (code == 0x4200 && output != 3) {
			return false
		}
		if code == 0x4200 {
			calibration, ok := read(index)
			if !ok || !calibration.matches(1, 3) {
				return false
			}
			index++
		}
	}
	tokenizer, ok := read(index)
	return hasConfidence && ok && tokenizer.dtype == 4 && index+1 == count
}

type cactTensor struct {
	dtype byte
	shape []uint64
	data  []byte
}

func (tensor cactTensor) matches(dtype byte, shape ...uint64) bool {
	return tensor.dtype == dtype && slices.Equal(tensor.shape, shape)
}

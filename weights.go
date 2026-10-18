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

// A .cact archive, as written by upstream needle/model/export.py, is a header
// of 48 little-endian uint32 fields and one float32 (_HDR_FMT "<48If"), a
// float32 codebook, a directory of 44-byte tensor records (_REC_FMT
// "<BBHIIIIQQII") and then the tensor data.
const (
	cactHeaderSize = 49 * 4
	cactRecordSize = 44

	// Header field indexes.
	cactTensorCount  = 1  // num_tensors
	cactCodebookSize = 2  // float32 codebook entries following the header
	cactModelWidth   = 7  // d_model
	cactLayers       = 10 // num_layers
	cactQKVConvTaps  = 19 // qkv_conv_taps; nonzero adds three tap tensors per layer
	cactEngramSites  = 31 // number of engram layers, at most cactMaxEngramSites

	cactMaxEngramSites = 16

	// Tensor record byte offsets.
	recDType  = 0  // uint8
	recNDim   = 1  // uint8
	recPad    = 2  // uint16, always zero
	recShape  = 4  // four uint32 dimensions, zero beyond ndim
	recOffset = 20 // uint64 byte offset of the tensor data
	recSize   = 28 // uint64 byte length of the tensor data
	recGroup  = 36 // uint32 CQ group size
	recBits   = 40 // uint32 CQ index width

	// Tensor dtypes.
	dtypeFP16 = 1
	dtypeFP32 = 2
	dtypeCQ   = 3
	dtypeRAW  = 4

	// Tensors before the optional head manifest or the speech manifest:
	// embedding, the per-layer blocks, mHC, permutations, the per-site
	// engrams and final_norm. Per-layer and per-site tensors are counted
	// separately from the fixed ones.
	cactFixedTensors    = 13
	cactLayerTensors    = 24
	cactLayerTapTensors = 3
	cactEngramTensors   = 4

	// heads.manifest codes, stored as the FP16 values 1, 2 and 3.
	headEmbedding  = 0x3c00
	headConfidence = 0x4000
	headRouter     = 0x4200
	maxHeads       = 3

	// audio.manifest holds 13 float32 values: a magic number, a layout
	// version, and the speech geometry, with the sample rate at index 9.
	audioManifestLength  = 13
	audioManifestMagic   = 3246
	audioManifestVersion = 1
	audioManifestRate    = 9
)

func cactField(blob []byte, index int) uint64 {
	return uint64(binary.LittleEndian.Uint32(blob[index*4:]))
}

// manifestPosition locates the tensor record after final_norm: the head
// manifest of a text archive or the audio manifest of a speech archive.
func manifestPosition(blob []byte) (directory, count, index uint64, ok bool) {
	if len(blob) < cactHeaderSize || binary.LittleEndian.Uint32(blob) != needle3WeightsTag {
		return
	}
	count = cactField(blob, cactTensorCount)
	directory = cactHeaderSize + cactField(blob, cactCodebookSize)*4
	layers, sites := cactField(blob, cactLayers), cactField(blob, cactEngramSites)
	if directory > uint64(len(blob)) || count > (uint64(len(blob))-directory)/cactRecordSize ||
		cactField(blob, cactModelWidth) == 0 || layers == 0 || sites > cactMaxEngramSites {
		return
	}
	perLayer := uint64(cactLayerTensors)
	if cactField(blob, cactQKVConvTaps) != 0 {
		perLayer += cactLayerTapTensors
	}
	index = cactFixedTensors + layers*perLayer + sites*cactEngramTensors
	ok = index < count
	return
}

// headCodes decodes an FP16 heads.manifest. Codes must be strictly increasing
// and drawn from the known head kinds.
func headCodes(data []byte, n uint64) ([]uint16, bool) {
	codes := make([]uint16, 0, n)
	previous := uint16(0)
	for i := range n {
		code := binary.LittleEndian.Uint16(data[i*2:])
		if code <= previous || (code != headEmbedding && code != headConfidence && code != headRouter) {
			return nil, false
		}
		previous = code
		codes = append(codes, code)
	}
	return codes, true
}

// weightsKind distinguishes text and speech before needle_load can replace
// either process-global model. The shared format tag does not identify the kind.
func weightsKind(blob []byte) int {
	directory, count, index, ok := manifestPosition(blob)
	if !ok {
		return 0
	}
	record := blob[directory+index*cactRecordSize : directory+(index+1)*cactRecordSize]
	if binary.LittleEndian.Uint16(record[recPad:]) != 0 || binary.LittleEndian.Uint32(record[recGroup:]) != 0 || binary.LittleEndian.Uint32(record[recBits:]) != 0 {
		return 0
	}
	offset, size := binary.LittleEndian.Uint64(record[recOffset:]), binary.LittleEndian.Uint64(record[recSize:])
	if offset < directory+count*cactRecordSize || offset > uint64(len(blob)) || size == 0 || size > uint64(len(blob))-offset {
		return 0
	}
	shape := binary.LittleEndian.Uint32(record[recShape:])
	for i := 1; i < 4; i++ {
		if binary.LittleEndian.Uint32(record[recShape+i*4:]) != 0 {
			return 0
		}
	}
	data := blob[offset : offset+size]
	dtype, ndim := record[recDType], record[recNDim]
	if dtype == dtypeRAW && ndim == 0 && shape == 0 && index+1 == count {
		return modelText // Text archive without probe heads: only the tokenizer follows.
	}
	if dtype == dtypeFP16 && ndim == 1 && shape > 0 && shape <= maxHeads && size == uint64(shape)*2 {
		if _, ok := headCodes(data, uint64(shape)); ok {
			return modelText
		}
		return 0
	}
	// Recognize the published version-1 audio manifest only; extend this
	// classifier when upstream publishes another speech archive layout.
	if dtype == dtypeFP32 && ndim == 1 && shape == audioManifestLength && size == audioManifestLength*4 && index+1 < count {
		value := func(i int) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:])) }
		if value(0) == audioManifestMagic && value(1) == audioManifestVersion && value(audioManifestRate) == SpeechSampleRate {
			return modelSpeech
		}
	}
	return 0
}

// confidenceHeadPresent inspects only the documented v3 head metadata. The
// native loader validates the model itself; unknown head layouts fail closed.
func confidenceHeadPresent(blob []byte) bool {
	directory, count, index, ok := manifestPosition(blob)
	if !ok || index+2 > count {
		return false
	}
	width, layers := cactField(blob, cactModelWidth), cactField(blob, cactLayers)
	previousEnd := directory + count*cactRecordSize
	read := func(index uint64) (cactTensor, bool) {
		if index >= count {
			return cactTensor{}, false
		}
		record := blob[directory+index*cactRecordSize : directory+(index+1)*cactRecordSize]
		tensor := cactTensor{dtype: record[recDType]}
		ndim := int(record[recNDim])
		if ndim > 4 || binary.LittleEndian.Uint16(record[recPad:]) != 0 {
			return tensor, false
		}
		for i := range 4 {
			size := uint64(binary.LittleEndian.Uint32(record[recShape+i*4:]))
			if i < ndim {
				if size == 0 {
					return tensor, false
				}
				tensor.shape = append(tensor.shape, size)
			} else if size != 0 {
				return tensor, false
			}
		}
		offset, size := binary.LittleEndian.Uint64(record[recOffset:]), binary.LittleEndian.Uint64(record[recSize:])
		if offset < previousEnd || offset > uint64(len(blob)) || size > uint64(len(blob))-offset || size == 0 {
			return tensor, false
		}
		group, bits := uint64(binary.LittleEndian.Uint32(record[recGroup:])), binary.LittleEndian.Uint32(record[recBits:])
		switch tensor.dtype {
		case dtypeFP16:
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
		case dtypeCQ: // Current probe matrices are CQ4, with padded input groups.
			if ndim != 2 || bits != 4 || group < 2 || group&(group-1) != 0 {
				return tensor, false
			}
			groups := (tensor.shape[1] + group - 1) / group
			bytesPerRow := groups * (group/2 + 2)
			if tensor.shape[0] > size/bytesPerRow || size != tensor.shape[0]*bytesPerRow {
				return tensor, false
			}
		case dtypeRAW: // Tokenizer.
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
	if !ok || !finalNorm.matches(dtypeFP16, width) {
		return false
	}
	manifest, ok := read(index)
	if !ok || manifest.dtype != dtypeFP16 || len(manifest.shape) != 1 || manifest.shape[0] > maxHeads {
		return false
	}
	codes, ok := headCodes(manifest.data, manifest.shape[0])
	if !ok {
		return false
	}
	index++
	hasConfidence := false
	for _, code := range codes {
		hasConfidence = hasConfidence || code == headConfidence
		// Each head is [probes, gain, query, row_bias, proj, bias]; the
		// router appends a calibration vector.
		var head [6]cactTensor
		for j := range head {
			head[j], ok = read(index)
			if !ok {
				return false
			}
			index++
		}
		if head[1].dtype != dtypeFP16 || len(head[1].shape) != 2 || len(head[2].shape) != 2 || len(head[5].shape) != 1 {
			return false
		}
		k, q, output := head[1].shape[1], head[2].shape[0], head[5].shape[0]
		matrixType := head[0].dtype
		// Probes are CQ ((L+1)*k, d_model), or FP16 (L+1, k, d_model) in the
		// earlier all-FP16 layout.
		probes := head[0].matches(dtypeCQ, (layers+1)*k, width) || head[0].matches(dtypeFP16, layers+1, k, width)
		if !probes || !head[1].matches(dtypeFP16, layers+1, k) || !head[2].matches(matrixType, q, width) ||
			!head[3].matches(dtypeFP16, q, layers+1, k) || !head[4].matches(matrixType, output, q*width) || !head[5].matches(dtypeFP16, output) ||
			(code == headConfidence && output != 1) || (code == headRouter && output != 3) {
			return false
		}
		if code == headRouter {
			calibration, ok := read(index)
			if !ok || !calibration.matches(dtypeFP16, 3) {
				return false
			}
			index++
		}
	}
	tokenizer, ok := read(index)
	return hasConfidence && ok && tokenizer.dtype == dtypeRAW && index+1 == count
}

type cactTensor struct {
	dtype byte
	shape []uint64
	data  []byte
}

func (tensor cactTensor) matches(dtype byte, shape ...uint64) bool {
	return tensor.dtype == dtype && slices.Equal(tensor.shape, shape)
}

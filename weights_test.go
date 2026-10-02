package needle

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestConfidenceHeadPresent(t *testing.T) {
	t.Parallel()
	for _, quantized := range []bool{false, true} {
		for _, heads := range [][]uint16{nil, {0x3c00}, {0x4000}, {0x4200}, {0x3c00, 0x4000}, {0x4000, 0x4200}, {0x3c00, 0x4000, 0x4200}} {
			want := false
			for _, code := range heads {
				want = want || code == 0x4000
			}
			if got := confidenceHeadPresent(testHeadArchive(heads, quantized)); got != want {
				t.Errorf("heads=%x quantized=%v: got %v, want %v", heads, quantized, got, want)
			}
		}
	}
	const directory, manifest = 196 + 28*4, 13 + 2*27
	mutations := map[string]func([]byte){
		"wrong generation":               func(b []byte) { binary.LittleEndian.PutUint32(b, needle2WeightsTag) },
		"huge tensor count":              func(b []byte) { binary.LittleEndian.PutUint32(b[4:], ^uint32(0)) },
		"huge codebook":                  func(b []byte) { binary.LittleEndian.PutUint32(b[8:], ^uint32(0)) },
		"huge layer count":               func(b []byte) { binary.LittleEndian.PutUint32(b[10*4:], ^uint32(0)) },
		"zero width":                     func(b []byte) { binary.LittleEndian.PutUint32(b[7*4:], 0) },
		"unknown geometry":               func(b []byte) { binary.LittleEndian.PutUint32(b[31*4:], 17) },
		"wrong final norm":               func(b []byte) { b[directory+(manifest-1)*44] = 4 },
		"wrong manifest dtype":           func(b []byte) { b[directory+manifest*44] = 2 },
		"wrong manifest rank":            func(b []byte) { b[directory+manifest*44+1] = 5 },
		"manifest points into directory": func(b []byte) { binary.LittleEndian.PutUint64(b[directory+manifest*44+20:], 196) },
		"manifest offset overflow":       func(b []byte) { binary.LittleEndian.PutUint64(b[directory+manifest*44+20:], ^uint64(0)) },
		"manifest length overflow":       func(b []byte) { binary.LittleEndian.PutUint64(b[directory+manifest*44+28:], ^uint64(0)) },
		"unknown manifest code": func(b []byte) {
			offset := binary.LittleEndian.Uint64(b[directory+manifest*44+20:])
			binary.LittleEndian.PutUint16(b[offset:], 0x4001)
		},
		"bad probe shape":            func(b []byte) { binary.LittleEndian.PutUint32(b[directory+(manifest+1)*44+4:], ^uint32(0)) },
		"bad quantization width":     func(b []byte) { binary.LittleEndian.PutUint32(b[directory+(manifest+1)*44+40:], 2) },
		"zero quantization group":    func(b []byte) { binary.LittleEndian.PutUint32(b[directory+(manifest+1)*44+36:], 0) },
		"bad gain size":              func(b []byte) { binary.LittleEndian.PutUint64(b[directory+(manifest+2)*44+28:], 2) },
		"wrong router calibration":   func(b []byte) { binary.LittleEndian.PutUint32(b[directory+(manifest+13)*44+4:], 2) },
		"missing router calibration": func(b []byte) { binary.LittleEndian.PutUint32(b[4:], manifest+14) },
		"wrong tokenizer":            func(b []byte) { b[directory+(manifest+14)*44] = 1 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			blob := testHeadArchive([]uint16{0x4000, 0x4200}, true)
			mutate(blob)
			if confidenceHeadPresent(blob) {
				t.Fatal("malformed head metadata granted calibration")
			}
		})
	}
	for _, heads := range [][]uint16{{0x4000, 0x4000}, {0x4000, 0x3c00}, {0x4000, 0x0000}, {0x4000, 0x7e00}} {
		if confidenceHeadPresent(testHeadArchive(heads, false)) {
			t.Errorf("noncanonical manifest %x granted calibration", heads)
		}
	}
	blob := testHeadArchive([]uint16{0x4000}, true)
	for size := range len(blob) {
		if confidenceHeadPresent(blob[:size]) {
			t.Fatalf("truncated archive of %d bytes granted calibration", size)
		}
	}
}

func FuzzConfidenceHeadPresent(f *testing.F) {
	f.Add(testHeadArchive([]uint16{0x4000}, true))
	f.Add(testHeadArchive([]uint16{0x3c00, 0x4000, 0x4200}, false))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, blob []byte) { confidenceHeadPresent(blob) })
}

func TestCustomWeightsConfidenceUsesLoadedBytes(t *testing.T) {
	t.Parallel()
	for _, calibrated := range []bool{false, true} {
		heads := []uint16{0x3c00}
		if calibrated {
			heads = []uint16{0x4000, 0x4200}
		}
		path := filepath.Join(t.TempDir(), "custom.cact")
		if err := os.WriteFile(path, testHeadArchive(heads, true), 0o600); err != nil {
			t.Fatal(err)
		}
		fake := &fakeNative{}
		first, runtime := newTestAgent(t, fake, Config{WeightsPath: path})
		// The same path must use retained metadata even if it has been replaced.
		replacement := []uint16{0x4000}
		if calibrated {
			replacement = nil
		}
		if err := os.WriteFile(path, testHeadArchive(replacement, true), 0o600); err != nil {
			t.Fatal(err)
		}
		second, err := prepareAgent(Config{WeightsPath: path})
		if err != nil {
			t.Fatal(err)
		}
		second.runtime = runtime
		for _, a := range []*agent{second, first} {
			fake.responses = append(fake.responses, []byte(`{"type":"call","confidence":0.9}`))
			response, err := a.Complete(context.Background(), "hello", 1)
			if err != nil || (response.Confidence != nil) != calibrated {
				t.Fatalf("calibrated=%v response=%+v err=%v", calibrated, response, err)
			}
		}
		if fake.loads != 1 {
			t.Fatalf("loads=%d, want 1", fake.loads)
		}
		// Loading another archive and switching back must refresh calibration.
		otherPath := filepath.Join(t.TempDir(), "other.cact")
		if err := os.WriteFile(otherPath, testHeadArchive(heads, true), 0o600); err != nil {
			t.Fatal(err)
		}
		other, err := prepareAgent(Config{WeightsPath: otherPath})
		if err != nil {
			t.Fatal(err)
		}
		other.runtime = runtime
		for _, a := range []*agent{other, first} {
			fake.responses = append(fake.responses, []byte(`{"type":"call","confidence":0.9}`))
			response, err := a.Complete(context.Background(), "hello", 1)
			want := calibrated
			if a == first {
				want = !calibrated
			}
			if err != nil || (response.Confidence != nil) != want {
				t.Fatalf("want calibration=%v response=%+v err=%v", want, response, err)
			}
		}
	}
}

// A tiny metadata archive following export.py's CQ4 and earlier FP16 head
// layouts. The preceding model tensors are placeholders; native tests use the
// published archive rather than passing this metadata fixture to the engine.
func testHeadArchive(heads []uint16, quantized bool) []byte {
	tensors := make([]cactTensor, 0)
	add := func(dtype byte, shape ...uint64) { tensors = append(tensors, cactTensor{dtype: dtype, shape: shape}) }
	for range 13 + 2*27 - 1 {
		add(1, 1)
	}
	add(1, 4) // final_norm
	if len(heads) > 0 {
		add(1, uint64(len(heads)))
	}
	for _, code := range heads {
		output := uint64(2)
		if code == 0x4000 {
			output = 1
		} else if code == 0x4200 {
			output = 3
		}
		matrixType := byte(1)
		if quantized {
			matrixType = 3
			add(3, 3, 4)
		} else {
			add(1, 3, 1, 4)
		}
		add(1, 3, 1)
		add(matrixType, 2, 4)
		add(1, 2, 3, 1)
		add(matrixType, output, 8)
		add(1, output)
		if code == 0x4200 {
			add(1, 3)
		}
	}
	add(4)
	const directory = 196 + 28*4
	blob := make([]byte, directory+44*len(tensors))
	for field, value := range map[int]uint32{0: needle3WeightsTag, 1: uint32(len(tensors)), 2: 28, 7: 4, 10: 2, 19: 3} {
		binary.LittleEndian.PutUint32(blob[field*4:], value)
	}
	for index, tensor := range tensors {
		blob = append(blob, make([]byte, (64-len(blob)%64)%64)...)
		offset := len(blob)
		size := uint64(2)
		for _, dimension := range tensor.shape {
			size *= dimension
		}
		if tensor.dtype == 3 {
			size = tensor.shape[0] * ((tensor.shape[1] + 3) / 4) * 4
		} else if tensor.dtype == 4 {
			size = 3
		}
		blob = append(blob, make([]byte, size)...)
		record := blob[directory+index*44:]
		record[0], record[1] = tensor.dtype, byte(len(tensor.shape))
		for dimension, value := range tensor.shape {
			binary.LittleEndian.PutUint32(record[4+dimension*4:], uint32(value))
		}
		binary.LittleEndian.PutUint64(record[20:], uint64(offset))
		binary.LittleEndian.PutUint64(record[28:], size)
		if tensor.dtype == 3 {
			binary.LittleEndian.PutUint32(record[36:], 4)
			binary.LittleEndian.PutUint32(record[40:], 4)
		}
		if index == 13+2*27 {
			for i, code := range heads {
				binary.LittleEndian.PutUint16(blob[offset+i*2:], code)
			}
		}
	}
	return blob
}

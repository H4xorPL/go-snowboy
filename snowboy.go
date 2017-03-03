package snowboy

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/Kitt-AI/snowboy/swig/Go"
)

type snowboyResult int
const (
	snowboyResultSilence snowboyResult = -2
	snowboyResultError                 = -1
	snowboyResultNoDetection           = 0
)

// Detector is holds the context and base impl for snowboy audio detection
type Detector struct {
	raw              snowboydetect.SnowboyDetect
	initialized      bool
	handlers         map[snowboyResult]handlerKeyword
	modelStr         string
	sensitivityStr   string
	silenceThreshold time.Duration
	silenceElapsed   time.Duration
	ResourceFile     string
	AudioGain        float32
}

// Creates a standard Detector from a resources file
// Gives a default gain of 1.0
func NewDetector(resourceFile string) Detector {
	return Detector{
		ResourceFile: resourceFile,
		AudioGain: 1.0,
	}
}

// Close handles cleanup required by snowboy library
//
// Clients must call Close on detectors after doing any detection
// Returns error if Detector was never used
func (d *Detector) Close() error {
	if d.initialized {
		d.initialized = false
		snowboydetect.DeleteSnowboyDetect(d.raw)
		return nil
	} else {
		return errors.New("snowboy not initialize")
	}
}

// Install a handler for the given hotword
func (d *Detector) Handle(hotword Hotword, handler Handler) {
	if len(d.handlers) > 0 {
		d.modelStr += ","
		d.sensitivityStr += ","
	}
	d.modelStr += hotword.Model
	d.sensitivityStr += strconv.FormatFloat(float64(hotword.Sensitivity), 'f', 2, 64)
	if d.handlers == nil {
		d.handlers = make(map[snowboyResult]handlerKeyword)
	}
	d.handlers[snowboyResult(len(d.handlers) + 1)] = handlerKeyword{
		Handler: handler,
		keyword: hotword.Name,
	}
}

// Installs a handle for the given hotword based on the func argument
// instead of the Handler interface
func (d *Detector) HandleFunc(hotword Hotword, handler func(string)) {
	d.Handle(hotword, handlerFunc(handler))
}

// Install a handler for when silence is detected
func (d *Detector) HandleSilence(threshold time.Duration, handler Handler) {
	if d.handlers == nil {
		d.handlers = make(map[snowboyResult]handlerKeyword)
	}
	d.silenceThreshold = threshold
	d.handlers[snowboyResultSilence] = handlerKeyword{
		Handler: handler,
		keyword: "silence",
	}
}

// Installs a handle for when silence is detected based on the func argument
// instead of the Handler interface
func (d *Detector) HandleSilenceFunc(threshold time.Duration, handler func(string)) {
	d.HandleSilence(threshold, handlerFunc(handler))
}

// Reads from data and calls previously installed handlers when detection occurs
func (d *Detector) ReadAndDetect(data io.Reader) error {
	d.initialize()
	bytes := make([]byte, 2048)
	for {
		n, err := data.Read(bytes)
		if err != nil {
			if err == io.EOF {
				// Run detection on remaining bytes
				return d.route(d.runDetection(bytes))
			}
			return err
		}
		if n == 0 {
			// No data to read yet, but not eof so wait and try later
			time.Sleep(300 * time.Millisecond)
			continue
		}
		err = d.route(d.runDetection(bytes))
		if err != nil {
			return err
		}
	}
}

func (d *Detector) AudioFormat() (sampleRate, numChannels, bitsPerSample int) {
	d.initialize()
	sampleRate = d.raw.SampleRate()
	numChannels = d.raw.NumChannels()
	bitsPerSample = d.raw.BitsPerSample()
	return
}

func (d *Detector) initialize() {
	if d.initialized {
		return
	}
	d.raw = snowboydetect.NewSnowboyDetect(d.ResourceFile, d.modelStr)
	d.raw.SetSensitivity(d.sensitivityStr)
	d.raw.SetAudioGain(d.AudioGain)
	d.initialized = true
}

func (d *Detector) route(result snowboyResult) error {
	if result == snowboyResultError {
		return SnowboyLibraryError
	} else if result != snowboyResultNoDetection {
		handlerKeyword, ok := d.handlers[result]
		if ok {
			if result == snowboyResultSilence && d.silenceElapsed < d.silenceThreshold {
				// Skip silence callback because threshold has not be surpassed
				return nil
			}
			// Reset silence elapse because it's got called
			d.silenceElapsed = 0
			handlerKeyword.call()
		} else {
			return NoHandler
		}
	}
	return nil
}

func (d *Detector) runDetection(data []byte) snowboyResult {
	if len(data) == 0 {
		return 0
	}
	ptr := snowboydetect.SwigcptrInt16_t(unsafe.Pointer(&data[0]))
	result := snowboyResult(d.raw.RunDetection(ptr, len(data) / 2 /* len of int16 */))
	if result == snowboyResultSilence {
		sampleRate, numChannels, bitDepth := d.AudioFormat()
		dataElapseTime := len(data) * int(time.Second) / (numChannels * (bitDepth / 8) * sampleRate)
		d.silenceElapsed += time.Duration(dataElapseTime)
	} else {
		// Reset silence elapse duration because non-silence was detected
		d.silenceElapsed = 0
	}
	return result
}

var NoHandler = errors.New("No handler installed")
var SnowboyLibraryError = errors.New("snowboy library error")

// A Handler is used to handle when keywords are detected
//
// Detected will be call with the keyword string
type Handler interface {
	Detected(string)
}

type handlerKeyword struct {
	Handler
	keyword string
}

func (h handlerKeyword) call() {
	h.Handler.Detected(h.keyword)
}

type handlerFunc func(string)

func (f handlerFunc) Detected(keyword string) {
	f(keyword)
}

// A Hotword represents a model filename and sensitivity for a snowboy detectable word
//
// Model is the filename for the .umdl file
//
// Sensitivity is the sensitivity of this specific hotword
//
// Name is what will be used in calls to Handler.Detected(string)
type Hotword struct {
	Model       string
	Sensitivity float32
	Name        string
}

// Creates a hotword from model only, parsing the hotward name from the model filename
// and using a sensitivity of 0.5
func NewDefaultHotword(model string) Hotword {
	return NewHotword(model, 0.5)
}

// Creates a hotword from model and sensitivity only, parsing
// the hotward name from the model filename
func NewHotword(model string, sensitivity float32) Hotword {
	h := Hotword{
		Model: model,
		Sensitivity: sensitivity,
	}
	name := strings.TrimRight(model, ".umdl")
	nameParts := strings.Split(name, "/")
	h.Name = nameParts[len(nameParts) - 1]
	return h
}

package live

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/eluv-io/errors-go"
)

func NewLiveSource() *LiveSource {
	port := rand.Intn(10000)
	port += 10000
	ls := &LiveSource{
		Port: port,
	}
	return ls
}

type LiveSource struct {
	Port int `json:"port,omitempty"`
	Pid  int `json:"pid,omitempty"`
	cmd  *exec.Cmd
}

func (l *LiveSource) Start() (err error) {
	log.Debug("In LiveSource start")
	e := errors.Template("start live source", errors.K.IO)

	if l.cmd != nil {
		return e("reason", "already started")
	}

	var ffmpeg string

	if toolchain, ok := os.LookupEnv("FFMPEG_DIST"); ok {
		ffmpeg = filepath.Join(toolchain, "bin/ffmpeg")
		if _, err = os.Stat(ffmpeg); err != nil {
			log.Warn("ffmpeg in FFMPEG_DIST not found", "command", ffmpeg)
			ffmpeg = ""
		} else {
			log.Debug("using ffmpeg from FFMPEG_DIST", "command", ffmpeg)
		}
	}

	if ffmpeg == "" {
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			log.Error("Failed to find ffmpeg binary, check ELV_TOOLCHAIN env variable")
			return e(err, "reason", "failed to find ffmpeg binary, check ELV_TOOLCHAIN env variable")
		}
		log.Debug("using system ffmpeg", "command", ffmpeg)
	}

	sourceUrl := fmt.Sprintf("udp://127.0.0.1:%d", l.Port)

	log.Info("starting live source", "url", sourceUrl)

	// i.e ffmpeg -re -i media/FS1-19-10-15-70sec.ts -c copy -f mpegts udp://127.0.0.1:21001?pkt_size=1316
	l.cmd = exec.Command(ffmpeg,
		"-re",
		"-i",
		"../media/BBB4_HD_51_AVC_120s_CCBYblendercloud.ts",
		"-c",
		"copy",
		"-f",
		"mpegts",
		sourceUrl+"?pkt_size=1316")
	l.cmd.Stdout = nil
	l.cmd.Stderr = nil

	err = l.cmd.Start()
	if err != nil {
		log.Error("Failed to start live source", "port", l.Port)
		return e(err)
	}

	l.Pid = l.cmd.Process.Pid
	go func() {
		err := l.cmd.Wait()
		if err != nil {
			log.Error("Failed to run command", err, "pid", l.Pid, "cmd", fmt.Sprintf("%s %s", l.cmd.Path, l.cmd.Args))
		}
	}()

	return nil
}

func (l *LiveSource) Stop() (err error) {
	e := errors.Template("stop live source", errors.K.IO)
	var process *os.Process

	if l.cmd == nil {
		if l.Pid == 0 {
			// source was never started...
			return
		}
		// LiveSource was unmarshalled or created with its PID
		// find the corresponding process...
		process, err = os.FindProcess(l.Pid)
		if err != nil {
			return e(err)
		}
	} else {
		process = l.cmd.Process
	}

	err = process.Kill()
	if err == nil {
		err = process.Release()
	}

	return e.IfNotNil(err)
}

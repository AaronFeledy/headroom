package contract

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

var fixtureExecutable string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	defer func() {
		if activationTools.manager != "" {
			_ = os.RemoveAll(filepath.Dir(activationTools.manager))
		}
	}()
	if executable := os.Getenv("HEADROOM_CRASH_HELPER_FIXTURE"); executable != "" {
		fixtureExecutable = executable
		return m.Run()
	}
	if runtime.GOOS == "darwin" {
		// macOS commonly advertises /var/folders via the /var symlink. Keep
		// fixtures on real paths; production install roots still reject links.
		temp, err := filepath.EvalSymlinks(os.TempDir())
		if err != nil || os.Setenv("TMPDIR", temp) != nil {
			fmt.Fprintln(os.Stderr, "cannot canonicalize macOS test directory", err)
			return 2
		}
	}
	root, err := os.MkdirTemp("", "headroom-native-fixture-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer os.RemoveAll(root)
	ext := ".exe"
	if runtime.GOOS != "windows" {
		ext = ""
	}
	fixtureExecutable = filepath.Join(root, "headroom-fixture"+ext)
	source := filepath.Join(root, "main.go")
	program := `package main
import ("encoding/json"; "os"; "path/filepath"; "strconv"; "strings"; "time")
func main() {
 var ready string; sleep:=os.Getenv("HEADROOM_FIXTURE_SLEEP_MS"); log:=os.Getenv("HEADROOM_FIXTURE_LAUNCH_LOG")
 for i:=1; i<len(os.Args); i++ { if i+1<len(os.Args) { switch os.Args[i] { case "--headroom-ready-file": ready=os.Args[i+1]; i++; case "--headroom-fixture-log": log=os.Args[i+1]; i++; case "--headroom-fixture-sleep": sleep=os.Args[i+1]; i++ } } }
 executable,_:=os.Executable(); directory:=filepath.Dir(filepath.Dir(executable)); if filepath.Base(directory)=="Contents" { directory=filepath.Dir(filepath.Dir(directory)) }; generation:=filepath.Base(directory); version:=strings.Split(generation,".generation-")[0]
 if os.Getenv("HEADROOM_FIXTURE_CRASH_VERSION")==version { os.Exit(8) }
 if ready!="" && os.Getenv("HEADROOM_FIXTURE_NO_READY_VERSION")!=version { b,_:=json.Marshal(map[string]any{"nonce":os.Getenv("HEADROOM_READY_NONCE"),"pid":os.Getpid(),"version":version,"executable":executable}); if log!="" { f,e:=os.OpenFile(log,os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); if e==nil { _,_=f.Write(append(b,'\n')); _=f.Close() } }; _=os.WriteFile(ready,append(b,'\n'),0600) }
 if evidence:=os.Getenv("USAGE_CONFIG"); evidence!="" && os.Getenv("HEADROOM_MANAGED_SERVICE_RESTART")!="" {
  if log!="" { b,_:=json.Marshal(map[string]any{"pid":os.Getpid(),"version":version,"executable":executable}); f,e:=os.OpenFile(log,os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); if e==nil { _,_=f.Write(append(b,'\n')); _=f.Close() } }
  cwd,_:=os.Getwd(); _=os.WriteFile(evidence,[]byte(cwd+"\n"+os.Getenv("USAGE_LISTEN_ADDR")+"\n"),0600)
  receipt:=filepath.Join(os.Getenv("HEADROOM_INSTALL_ROOT"),"runtime","managed-serve.json")
  for i:=0;i<250;i++ {
   data,e:=os.ReadFile(receipt)
   if e==nil {
    var value map[string]any
    // The previous process's receipt can remain until the parent publishes
    // this child's identity. Never acknowledge that stale receipt.
    if json.Unmarshal(data,&value)==nil && value["pid"]==float64(os.Getpid()) && value["launch_token"]==os.Getenv("HEADROOM_MANAGED_SERVICE_RESTART") {
     value["launch_token"]=""; value["ready"]=true; encoded,_:=json.Marshal(value)
     if os.WriteFile(receipt,append(encoded,'\n'),0600)==nil { break }
    }
   }
   time.Sleep(20*time.Millisecond)
  }
 }
 if sleep!="" { if n,e:=strconv.Atoi(sleep); e==nil { time.Sleep(time.Duration(n)*time.Millisecond) } }
 if os.Getenv("HEADROOM_FIXTURE_FAIL")!="" { os.Exit(9) }
}`
	if err = os.WriteFile(source, []byte(program), 0o600); err == nil {
		command := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", fixtureExecutable, source)
		command.Env = append(os.Environ(), "GOTOOLCHAIN=local")
		output, buildErr := command.CombinedOutput()
		if buildErr != nil {
			err = fmt.Errorf("compile native fixture: %w: %s", buildErr, output)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return m.Run()
}

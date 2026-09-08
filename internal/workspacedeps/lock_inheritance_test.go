//go:build darwin || linux

package workspacedeps

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Killing only the lock supervisor must not unlock an installation whose
// child still writes. FIFO readiness and release coordinate actual processes;
// no sleep or elapsed-time assumption establishes lock ownership.
func TestKernelLockSurvivesSupervisorDeathWhileChildRuns(t *testing.T) {
	locker := "flock"
	if runtime.GOOS == "darwin" {
		locker = "lockf"
	}
	if _, err := exec.LookPath(locker); err != nil {
		t.Skipf("%s is not installed", locker)
	}
	root := t.TempDir()
	fifo := filepath.Join(root, "release")
	if err := exec.CommandContext(testContext(t), "mkfifo", fifo).Run(); err != nil { //nolint:gosec // G204: fixed command creates a synthetic FIFO under t.TempDir.
		t.Fatal(err)
	}
	home := Home(root, "inheritance")
	lock := lockPath(home, "inheritance")
	childBody := "exec 8<> " + shellQuote(fifo) + "\nprintf '%s\\n' \"$$\"\nread release <&8\n"
	command := exec.CommandContext(testContext(t), "/bin/sh", "-c", scriptExecCommand) //nolint:gosec // G204: executes the production wrapper against synthetic test paths.
	command.Env = append(os.Environ(), buildEnv(RunSpec{
		DepID: "inheritance", Home: home, ShimDir: ShimDir(root), Platform: Platform{OS: runtime.GOOS},
	}, filepath.Join(root, "result.json"), time.Minute)...)
	command.Stdin = strings.NewReader(WrapScript("exec sh -c " + shellQuote(childBody)))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	released, waited := false, false
	releaseChild := func() error {
		writer, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: synthetic FIFO created under t.TempDir.
		if err != nil {
			return err
		}
		_, err = writer.WriteString("done\n")
		_ = writer.Close()
		if err == nil {
			released = true
		}
		return err
	}
	defer func() {
		if !released {
			_ = releaseChild()
		}
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("child did not reach FIFO readiness: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || childPID <= 0 || childPID == command.Process.Pid {
		t.Fatalf("invalid child identity %q: %v", line, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	waited = true
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("child must remain alive after only the supervisor was killed: %v", err)
	}
	contender := exec.CommandContext(testContext(t), "/bin/sh", "-c", lockProbeHelpers+"\nif memoh_lock_active "+shellQuote(lock)+"; then exit 75; fi") //nolint:gosec // G204: fixed lock probe with a shell-quoted synthetic path.
	err = contender.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 75 {
		t.Fatalf("living child lost the dependency lock after supervisor death: %v", err)
	}
	if err := releaseChild(); err != nil {
		t.Fatal(err)
	}
	// Blocking acquisition is the completion barrier for the released child.
	var barrier *exec.Cmd
	if runtime.GOOS == "darwin" {
		barrier = exec.CommandContext(testContext(t), "lockf", "-k", "-t", "5", lock, "true") //nolint:gosec // G204: fixed lock utility with a synthetic test path.
	} else {
		barrier = exec.CommandContext(testContext(t), "flock", "-w", "5", lock, "true") //nolint:gosec // G204: fixed lock utility with a synthetic test path.
	}
	if err := barrier.Run(); err != nil {
		t.Fatalf("lock was not released when the last child exited: %v", err)
	}
}

package application

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	subagentReapTimeout = 20 * time.Second
	// subagentReapLease outlasts an attempt and then spaces out the next one.
	subagentReapLease = subagentReapTimeout + 30*time.Second

	// subagentStopConfirmedNotice opens the first prompt after a run whose stop
	// Memoh confirmed only later, since that run may have done part of its work.
	subagentStopConfirmedNotice = "[Memoh] Your previous run in this session ended before Memoh could confirm that it stopped. Memoh has since stopped the processes it could find from this session, including any left running by earlier runs. Check the current workspace state before repeating actions that may already have completed."
)

// subagentProcessScript SIGKILLs every workspace process whose environment
// holds the marker, together with its descendants, until a scan finds none,
// and prints how many the last scan found. The tree is stopped before it is
// killed so that a child forked meanwhile is still found under its parent,
// which covers children that reset their environment; a tree that keeps
// growing through every round is reported unsettled. An exited process reads
// empty and stops counting. Not seen: a process that made itself non-dumpable
// (its environ needs CAP_SYS_PTRACE), and one that reset its environment
// outside the tree, such as a daemon or a marked process that ran exec env -i.
// A grep without -z would read every process as unmarked, and so would an
// unreadable /proc, so the script refuses to run with either.
const subagentProcessScript = `m='%s'
printf '%%s\0' "$m" | grep -qzxF -- "$m" || { echo 'grep -z unsupported'; exit 3; }
[ -r /proc/$$/environ ] || { echo '/proc unreadable'; exit 3; }
grow() {
	pairs=
	for s in /proc/[0-9]*/status; do
		p=${s#/proc/}; p=${p%%/status}
		while IFS=: read -r k v; do [ "$k" = PPid ] && { pairs="$pairs $p:$((v))"; break; }; done 2>/dev/null <"$s"
	done
	grown=1
	while [ "$grown" = 1 ]; do
		grown=0
		for pair in $pairs; do
			case $tree in *" ${pair#*:} "*) case $tree in *" ${pair%%:*} "*) ;; *) tree="$tree${pair%%:*} "; grown=1 ;; esac ;; esac
		done
	done
}
left=0
for i in 1 2 3 4 5 6 7 8 9 10; do
	tree=' '
	for e in /proc/[0-9]*/environ; do
		p=${e#/proc/}; p=${p%%/environ}
		grep -qzxF -- "$m" "$e" 2>/dev/null && tree="$tree$p "
	done
	left=0
	[ "$tree" = ' ' ] && break
	settled=
	for r in 1 2 3 4 5 6 7 8 9 10; do
		kill -STOP $tree 2>/dev/null
		before=$tree
		grow
		[ "$tree" = "$before" ] && { settled=1; break; }
	done
	kill -KILL $tree 2>/dev/null
	[ -n "$settled" ] || { echo unsettled; exit 0; }
	set -- $tree
	left=$#
	sleep 0.2
done
echo "$left"`

type nativeBridgeProvider interface {
	NativeMCPClient(ctx context.Context, botID string) (*bridge.Client, error)
}

// SubagentProcessReaper stops whatever in the bot's workspace still carries
// an external child session's marker. The marker is the child thread id, not
// a run id, since an ACP agent process serves many runs, so one reap also
// stops services that earlier, completed runs left. A clean reap is the best
// evidence of a stop Memoh has, not proof that every effect of the child
// ended.
type SubagentProcessReaper struct {
	bridges nativeBridgeProvider
}

func NewSubagentProcessReaper(bridges nativeBridgeProvider) *SubagentProcessReaper {
	return &SubagentProcessReaper{bridges: bridges}
}

// Reap returns nil once a scan of the live workspace finds no marked process
// of the child left; what the scan cannot see is listed on
// subagentProcessScript.
func (r *SubagentProcessReaper) Reap(ctx context.Context, botID, childID string) error {
	if _, err := uuid.Parse(childID); err != nil {
		return errs.Wrap(err, "parse subagent thread")
	}
	client, err := r.bridges.NativeMCPClient(ctx, botID)
	if err != nil {
		return errs.WrapDependency(err, "connect subagent workspace")
	}
	marker := external.SubagentThreadEnvEntry(childID)[0]
	result, err := client.Exec(ctx, fmt.Sprintf(subagentProcessScript, marker), "/", int32(subagentReapTimeout/time.Second)-5)
	if err != nil {
		return errs.WrapDependency(err, "scan subagent processes")
	}
	if left := strings.TrimSpace(result.Stdout); result.ExitCode != 0 || left != "0" {
		return errs.NewDependency("subagent processes remain", slog.Int("exit_code", int(result.ExitCode)), slog.String("left", left))
	}
	return nil
}

type subagentProcessReaperPort interface {
	Reap(ctx context.Context, botID, childID string) error
}

// SetSubagentProcessReaper enables confirming an unconfirmed termination.
// Without it such a child stays blocked and keeps its slot.
func (s *Service) SetSubagentProcessReaper(reaper subagentProcessReaperPort) {
	s.subagentReaper = reaper
}

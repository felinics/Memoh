package turnwire_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/agent/turn/turnwire"
)

// filledCommand sets every field of a StartTurnCommand, nested ones included,
// to a distinct non-zero value, so a field the wire type does not carry shows
// up as a difference after a round trip.
func filledCommand(t *testing.T) turn.StartTurnCommand {
	t.Helper()
	var cmd turn.StartTurnCommand
	fill(reflect.ValueOf(&cmd).Elem(), "cmd")
	cmd.SchemaVersion = turnwire.StartTurnVersion
	return cmd
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()

func fill(v reflect.Value, path string) {
	if v.Type() == rawMessageType {
		v.SetBytes([]byte(`{"path":"` + path + `"}`))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(path)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(int64(len(path)))
	case reflect.Struct:
		for i := range v.NumField() {
			fill(v.Field(i), path+"."+v.Type().Field(i).Name)
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), path)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0), path+"[0]")
	case reflect.Map:
		// JSON restores map values as their dynamic types; a string survives.
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(reflect.ValueOf("key"), reflect.ValueOf(any(path)))
	default:
		panic("fill: unsupported kind " + v.Kind().String() + " at " + path)
	}
}

func roundTrip(t *testing.T, cmd turn.StartTurnCommand) turn.StartTurnCommand {
	t.Helper()
	data, err := json.Marshal(turnwire.FromStartTurn(cmd))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire turnwire.StartTurn
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, err := wire.Command()
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	return got
}

// Every field of the command crosses. A field added to StartTurnCommand
// without a wire counterpart fails here instead of arriving empty in the
// other process.
func TestStartTurnCarriesEveryCommandField(t *testing.T) {
	cmd := filledCommand(t)
	if got := roundTrip(t, cmd); !reflect.DeepEqual(got, cmd) {
		t.Fatalf("round trip =\n%#v\nwant\n%#v", got, cmd)
	}
}

// The skill body crosses under explicit keys.
func TestStartTurnRequestedSkillShape(t *testing.T) {
	data, err := json.Marshal(turnwire.FromStartTurn(turn.StartTurnCommand{
		RequestedSkills: []turn.RequestedSkillContext{{
			Name: "skill", Description: "desc", Content: "body", SourceKind: "registry",
			OpaqueSourceID: "opaque", ContentHash: "hash", Identity: "identity",
		}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	const want = `[{"name":"skill","description":"desc","content":"body","source_kind":"registry","opaque_source_id":"opaque","content_hash":"hash","identity":"identity"}]`
	if got := string(fields["RequestedSkills"]); got != want {
		t.Fatalf("RequestedSkills = %s, want %s", got, want)
	}
}

// Payloads written before the wire type existed are the command's own JSON.
// They still decode, keeping every field that JSON carried; requested skills
// were stored as empty objects then and stay empty.
func TestStartTurnDecodesPayloadsOfThePreviousRelease(t *testing.T) {
	cmd := filledCommand(t)
	previous, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var wire turnwire.StartTurn
	if err := json.Unmarshal(previous, &wire); err != nil {
		t.Fatalf("unmarshal previous payload: %v", err)
	}
	got, err := wire.Command()
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	want := cmd
	want.RequestedSkills = []turn.RequestedSkillContext{{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded previous payload =\n%#v\nwant\n%#v", got, want)
	}
}

// The new form is readable by the previous release, which decodes into the
// command type itself: only the skill body, which it never read, differs.
func TestStartTurnIsReadableAsTheCommandJSON(t *testing.T) {
	cmd := filledCommand(t)
	data, err := json.Marshal(turnwire.FromStartTurn(cmd))
	if err != nil {
		t.Fatal(err)
	}
	var got turn.StartTurnCommand
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal as command: %v", err)
	}
	want := cmd
	want.RequestedSkills = []turn.RequestedSkillContext{{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded as command =\n%#v\nwant\n%#v", got, want)
	}
}

func TestStartTurnOmitsEmptyThreadID(t *testing.T) {
	data, err := json.Marshal(turnwire.FromStartTurn(turn.StartTurnCommand{SchemaVersion: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"ThreadID"`) {
		t.Fatalf("empty ThreadID serialized: %s", data)
	}
}

func TestCommandRejectsUnknownSchemaVersion(t *testing.T) {
	for _, version := range []int{0, 2, -1} {
		_, err := turnwire.StartTurn{SchemaVersion: version, TeamID: "team-1"}.Command()
		if !errors.Is(err, turnwire.ErrUnsupportedVersion) {
			t.Fatalf("version %d: err = %v, want %v", version, err, turnwire.ErrUnsupportedVersion)
		}
	}
}

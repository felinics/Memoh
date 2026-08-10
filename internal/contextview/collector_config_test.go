package contextview

import "testing"

type collectorConfigTestType struct {
	Value string
}

func TestCollectorConfigNilConfigReturnsZero(t *testing.T) {
	t.Parallel()

	got, err := collectorConfig[collectorConfigTestType](nil, "bad config")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != (collectorConfigTestType{}) {
		t.Fatalf("got = %#v, want zero value", got)
	}
}

func TestCollectorConfigDirectValue(t *testing.T) {
	t.Parallel()

	want := collectorConfigTestType{Value: "direct"}
	got, err := collectorConfig[collectorConfigTestType](want, "bad config")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != want {
		t.Fatalf("got = %#v, want %#v", got, want)
	}
}

func TestCollectorConfigPointerValue(t *testing.T) {
	t.Parallel()

	want := collectorConfigTestType{Value: "pointer"}
	got, err := collectorConfig[collectorConfigTestType](&want, "bad config")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != want {
		t.Fatalf("got = %#v, want %#v", got, want)
	}
}

func TestCollectorConfigNilPointerReturnsZero(t *testing.T) {
	t.Parallel()

	var nilPtr *collectorConfigTestType
	got, err := collectorConfig[collectorConfigTestType](nilPtr, "bad config")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != (collectorConfigTestType{}) {
		t.Fatalf("got = %#v, want zero value", got)
	}
}

func TestCollectorConfigWrongTypeReturnsError(t *testing.T) {
	t.Parallel()

	got, err := collectorConfig[collectorConfigTestType](42, "bad config")
	if err == nil || err.Error() != "bad config" {
		t.Fatalf("err = %v, want %q", err, "bad config")
	}
	if got != (collectorConfigTestType{}) {
		t.Fatalf("got = %#v, want zero value", got)
	}
}

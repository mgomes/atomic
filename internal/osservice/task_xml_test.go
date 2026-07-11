package osservice

import (
	"strings"
	"testing"
)

func TestMarshalTaskUsesInteractiveLeastPrivilegeToken(t *testing.T) {
	t.Parallel()

	data, err := marshalTask(
		"S-1-5-21-1-2-3-1001",
		`C:\Program Files\Ressik & Co\ressik.exe`,
		`--config "C:\Users\me\config.yaml" daemon`,
	)
	if err != nil {
		t.Fatalf("marshalTask() returned error: %v", err)
	}
	definition := string(data)
	for _, want := range []string{
		"<LogonType>InteractiveToken</LogonType>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<Interval>PT5M</Interval>",
		"<Count>255</Count>",
		`C:\Program Files\Ressik &amp; Co\ressik.exe`,
	} {
		if !strings.Contains(definition, want) {
			t.Errorf("task definition does not contain %q:\n%s", want, definition)
		}
	}
	if strings.Contains(strings.ToLower(definition), "password") {
		t.Errorf("task definition contains password material:\n%s", definition)
	}
}

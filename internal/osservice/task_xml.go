package osservice

import (
	"encoding/xml"
	"fmt"
)

type taskXML struct {
	XMLName          xml.Name         `xml:"Task"`
	Version          string           `xml:"version,attr"`
	XMLNS            string           `xml:"xmlns,attr"`
	RegistrationInfo taskRegistration `xml:"RegistrationInfo"`
	Triggers         taskTriggers     `xml:"Triggers"`
	Principals       taskPrincipals   `xml:"Principals"`
	Settings         taskSettings     `xml:"Settings"`
	Actions          taskActions      `xml:"Actions"`
}

type taskRegistration struct {
	Author      string `xml:"Author"`
	Description string `xml:"Description"`
}

type taskTriggers struct {
	Logon taskLogonTrigger `xml:"LogonTrigger"`
}

type taskLogonTrigger struct {
	Enabled bool   `xml:"Enabled"`
	UserID  string `xml:"UserId"`
}

type taskPrincipals struct {
	Principal taskPrincipal `xml:"Principal"`
}

type taskPrincipal struct {
	ID        string `xml:"id,attr"`
	UserID    string `xml:"UserId"`
	LogonType string `xml:"LogonType"`
	RunLevel  string `xml:"RunLevel"`
}

type taskSettings struct {
	MultipleInstances         string      `xml:"MultipleInstancesPolicy"`
	DisallowStartOnBatteries  bool        `xml:"DisallowStartIfOnBatteries"`
	StopIfGoingOnBatteries    bool        `xml:"StopIfGoingOnBatteries"`
	AllowHardTerminate        bool        `xml:"AllowHardTerminate"`
	StartWhenAvailable        bool        `xml:"StartWhenAvailable"`
	RunOnlyIfNetworkAvailable bool        `xml:"RunOnlyIfNetworkAvailable"`
	AllowStartOnDemand        bool        `xml:"AllowStartOnDemand"`
	Enabled                   bool        `xml:"Enabled"`
	Hidden                    bool        `xml:"Hidden"`
	RunOnlyIfIdle             bool        `xml:"RunOnlyIfIdle"`
	WakeToRun                 bool        `xml:"WakeToRun"`
	RestartOnFailure          taskRestart `xml:"RestartOnFailure"`
	ExecutionTimeLimit        string      `xml:"ExecutionTimeLimit"`
	Priority                  int         `xml:"Priority"`
}

type taskActions struct {
	Context string   `xml:"Context,attr"`
	Exec    taskExec `xml:"Exec"`
}

type taskRestart struct {
	Interval string `xml:"Interval"`
	Count    int    `xml:"Count"`
}

type taskExec struct {
	Command   string `xml:"Command"`
	Arguments string `xml:"Arguments"`
}

func marshalTask(userSID, executable, arguments string) ([]byte, error) {
	definition := taskXML{
		Version: "1.2",
		XMLNS:   "http://schemas.microsoft.com/windows/2004/02/mit/task",
		RegistrationInfo: taskRegistration{
			Author:      "Atomic",
			Description: "Runs scheduled Atomic backup plans for the signed-in user.",
		},
		Triggers: taskTriggers{Logon: taskLogonTrigger{Enabled: true, UserID: userSID}},
		Principals: taskPrincipals{Principal: taskPrincipal{
			ID:        "AtomicUser",
			UserID:    userSID,
			LogonType: "InteractiveToken",
			RunLevel:  "LeastPrivilege",
		}},
		Settings: taskSettings{
			MultipleInstances:  "IgnoreNew",
			AllowHardTerminate: true,
			StartWhenAvailable: true,
			AllowStartOnDemand: true,
			Enabled:            true,
			RestartOnFailure:   taskRestart{Interval: "PT5M", Count: 255},
			ExecutionTimeLimit: "PT0S",
			Priority:           7,
		},
		Actions: taskActions{
			Context: "AtomicUser",
			Exec:    taskExec{Command: executable, Arguments: arguments},
		},
	}
	data, err := xml.MarshalIndent(definition, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Windows task definition: %w", err)
	}
	return append([]byte(xml.Header), data...), nil
}

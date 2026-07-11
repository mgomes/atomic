package osservice

const userSystemdScript = `[Unit]
Description={{Description}}
ConditionFileIsExecutable={{Path | cmdEscape}}

[Service]
Type=simple
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
UMask=0077
Restart=on-failure
RestartSec=10s
TimeoutStopSec=30s

[Install]
WantedBy=default.target
`

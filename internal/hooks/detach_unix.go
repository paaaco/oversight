package hooks

import "syscall"

func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

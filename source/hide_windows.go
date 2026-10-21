package main

import (
	"os/exec"
	"syscall"
)

// Évite l'apparition d'une console noire au lancement des commandes
func hide(c *exec.Cmd) *exec.Cmd {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return c
}

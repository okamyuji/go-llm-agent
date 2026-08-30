//go:build unix

package fsx

import "syscall"

// noFollowFlag open 時にシンボリックリンクを辿らせないフラグ
const noFollowFlag = syscall.O_NOFOLLOW

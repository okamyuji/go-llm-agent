//go:build !unix

package fsx

// noFollowFlag O_NOFOLLOW を持たない OS では open 後の Stat 検査だけに頼る
const noFollowFlag = 0

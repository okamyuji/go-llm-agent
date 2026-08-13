package main

import "testing"

func TestRunExercise_AllScenariosPass(t *testing.T) {
	dir := t.TempDir()
	res, err := runExercise(dir)
	if err != nil {
		t.Fatalf("runExercise err=%v", err)
	}
	if !res.editBeforeReadDenied {
		t.Error("editBeforeReadDenied want true")
	}
	if !res.editAfterReadOK {
		t.Error("editAfterReadOK want true")
	}
	if !res.onlyTargetLineChanged {
		t.Error("onlyTargetLineChanged want true")
	}
	if !res.ambiguousMatchDenied {
		t.Error("ambiguousMatchDenied want true")
	}
}

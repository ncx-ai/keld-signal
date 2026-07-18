package eval

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCalibrationBinsAndECE(t *testing.T) {
	// 4 task_type predictions with hand-chosen confidences and correctness:
	//   conf 0.95 correct, 0.92 wrong  -> bin [0.9,1.0]: n=2, meanConf=0.935, acc=0.5
	//   conf 0.55 correct              -> bin [0.5,0.6): n=1, meanConf=0.55, acc=1.0
	//   conf 0.15 wrong                -> bin [0.1,0.2): n=1, meanConf=0.15, acc=0.0
	gold := []GoldRow{
		{TaskType: "codegen"}, {TaskType: "codegen"}, {TaskType: "codegen"}, {TaskType: "codegen"},
	}
	pred := []Pred{
		{TaskType: "codegen", Conf: map[string]float64{"task_type": 0.95}},
		{TaskType: "other", Conf: map[string]float64{"task_type": 0.92}},
		{TaskType: "codegen", Conf: map[string]float64{"task_type": 0.55}},
		{TaskType: "other", Conf: map[string]float64{"task_type": 0.15}},
	}
	r := Calibration(gold, pred, "task_type", 10)
	if r.N != 4 {
		t.Fatalf("N = %d, want 4", r.N)
	}
	var top *Bin
	for i := range r.Bins {
		if r.Bins[i].Lo == 0.9 {
			top = &r.Bins[i]
		}
	}
	if top == nil || top.Count != 2 || !approx(top.Accuracy, 0.5) || !approx(top.MeanConf, 0.935) {
		t.Fatalf("top bin wrong: %+v", top)
	}
	// ECE weights each bin by n_bin/N. Top bin has 2 preds (weight 2/4):
	//   (2/4)*|0.5-0.935| + (1/4)*|1-0.55| + (1/4)*|0-0.15|
	//   = 0.2175 + 0.1125 + 0.0375 = 0.3675
	if !approx(r.ECE, 0.3675) {
		t.Fatalf("ECE = %.5f, want 0.3675", r.ECE)
	}
}

func TestCalibrationSkipsBlankGold(t *testing.T) {
	gold := []GoldRow{{TaskType: ""}, {TaskType: "codegen"}}
	pred := []Pred{
		{TaskType: "codegen", Conf: map[string]float64{"task_type": 0.9}},
		{TaskType: "codegen", Conf: map[string]float64{"task_type": 0.9}},
	}
	if r := Calibration(gold, pred, "task_type", 10); r.N != 1 {
		t.Fatalf("blank gold must be excluded: N = %d, want 1", r.N)
	}
}

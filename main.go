package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"io/ioutil"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	dir = flag.String("dir", "", "Directory of files")
)

type opMeter struct {
	Num  uint64        //`json:"Count"`
	Time time.Duration //`json:"ExecTime"`
}

func gasCost(op vm.OpCode, blnum *big.Int) uint64 {
	switch op {
	case vm.STOP:
		return 0
	case vm.ADD, vm.SUB, vm.LT, vm.GT, vm.SLT, vm.SGT, vm.EQ, vm.ISZERO, vm.AND, vm.OR, vm.XOR, vm.NOT,
		vm.BYTE, vm.CALLDATALOAD:
		return vm.GasFastestStep
	case vm.MUL, vm.DIV, vm.SDIV, vm.MOD, vm.SMOD, vm.SIGNEXTEND:
		return vm.GasFastStep
	case vm.ADDMOD, vm.MULMOD, vm.JUMP:
		return vm.GasMidStep
	case vm.ADDRESS, vm.ORIGIN, vm.CALLER, vm.CALLVALUE, vm.CALLDATASIZE, vm.CODESIZE, vm.GASPRICE,
		vm.COINBASE, vm.TIMESTAMP, vm.NUMBER, vm.DIFFICULTY, vm.GASLIMIT, vm.POP, vm.PC, vm.MSIZE, vm.GAS:
		return vm.GasQuickStep
	case vm.BLOCKHASH:
		return vm.GasExtStep
	case vm.JUMPI:
		return vm.GasSlowStep
	case vm.JUMPDEST:
		return params.JumpdestGas

	}
	if op >= vm.PUSH1 && op <= vm.PUSH32 {
		return vm.GasFastestStep
	}

	if op >= vm.SWAP1 && op <= vm.SWAP16 {
		return vm.GasFastestStep
	}
	if op >= vm.DUP1 && op <= vm.DUP16 {
		return vm.GasFastestStep
	}

	var gt params.GasTable = params.GasTableHomestead

	if params.MainnetChainConfig.IsEIP150(blnum) {
		gt = params.GasTableEIP150
	}
	if params.MainnetChainConfig.IsEIP158(blnum) {
		gt = params.GasTableEIP158
	}
	if params.MainnetChainConfig.IsConstantinople(blnum) {
		gt = params.GasTableConstantinople
	}
	switch op {
	case vm.SLOAD:
		return gt.SLoad
	case vm.EXTCODESIZE:
		return gt.ExtcodeSize
	case vm.EXTCODECOPY:
		return gt.ExtcodeCopy
	case vm.BALANCE:
		return gt.Balance
	case vm.EXTCODEHASH:
		return gt.ExtcodeHash
	case vm.SHL, vm.SHR, vm.SAR:
		return vm.GasFastestStep
	case vm.CALL:
		return gt.Calls
	}

	return 0
}

type dataPoint struct {
	op          vm.OpCode
	blockNumber *big.Int
	count       uint64
	execTime    time.Duration
}

func (dp *dataPoint) gas() uint64 {
	return gasCost(dp.op, dp.blockNumber)
}
func (dp *dataPoint) totalGas() uint64 {
	return dp.count * dp.gas()
}
func (dp *dataPoint) mGasPerSec() float64 {
	// gas / nanos * 1 000 M = gas / s
	// (gas / 1000 000 ) / s = Mgas / s
	// (gas / 1M ) * 1000M / nanos = Mgas / s
	// (gas * 1000 ) / nanos = Mgas/s
	return float64(1000*dp.totalGas()) / float64(dp.execTime)
}
func (dp *dataPoint) String() string {

	return fmt.Sprintf("number %s, op %s; gas %d; count %d; execTime %d;execTime  %s; totalGas %d; MgasPerS %.03f",
		dp.blockNumber,
		dp.op.String(),
		dp.gas(),
		dp.count,
		dp.execTime,
		dp.execTime,
		dp.totalGas(),
		dp.mGasPerSec())
}
func (dp *dataPoint) CSV() string {

	return fmt.Sprintf("number;%s;opcode;%s;gas;%d;count;%d;time;%d;time;%s;totalgas;%d;mgaspers;%.03f",
		dp.blockNumber,
		dp.op.String(),
		dp.gas(),
		dp.count,
		dp.execTime,
		dp.execTime,
		dp.totalGas(),
		dp.mGasPerSec())
}

func (dp *dataPoint) Sub(prev *dataPoint) *dataPoint {
	return &dataPoint{
		blockNumber: dp.blockNumber,
		execTime:    dp.execTime - prev.execTime,
		count:       dp.count - prev.count,
		op:          dp.op,
	}
}

type statCollection struct {
	data map[int](map[vm.OpCode]*dataPoint)
}

func newStatCollection() statCollection {
	return statCollection{
		data: make(map[int](map[vm.OpCode]*dataPoint)),
	}
}
func (stats *statCollection) collect(blnum int, data []byte) error {

	var m [256]opMeter
	if err := json.Unmarshal(data, &m); err != nil {
		fmt.Printf("error: %v", err)
		return err
	}
	//fmt.Printf("OPCODE;GASCOST;COUNT;TOTALTIME;TOTALTIME;TOTALGAS;MGASPERNS\n")
	stats.data[blnum] = make(map[vm.OpCode]*dataPoint)
	for i := 0; i < 256; i++ {
		metric := m[i]

		if metric.Num > 0 {

			op := vm.OpCode(i)
			dp := &dataPoint{
				op:          op,
				blockNumber: new(big.Int).SetUint64(uint64(blnum)),
				count:       metric.Num,
				execTime:    metric.Time,
			}
			stats.data[blnum][op] = dp
		}
	}
	return nil
}

func (stats *statCollection) chartMGasPerS() {
	var opcodes = []vm.OpCode{vm.SLOAD, vm.BALANCE}
	fmt.Printf("Number;")
	for _, op := range opcodes {
		fmt.Printf("%s;", op.String())
	}
	fmt.Println("")
	// To store the numbers in slice in sorted order
	var numbers []int
	for k := range stats.data {
		numbers = append(numbers, k)
	}
	sort.Ints(numbers)

	var prevBlock map[vm.OpCode]*dataPoint

	for _, number := range numbers {
		block := stats.data[number]
		if prevBlock != nil {
			fmt.Printf("%v;", number)
			for _, op := range opcodes {
				dp := block[op]
				prevDp := prevBlock[op]
				modDp := dp.Sub(prevDp)
				fmt.Printf("%.02f;", modDp.mGasPerSec())

				//fmt.Printf("%s\n", modDp.CSV())
			}
			fmt.Println("")
		}
		prevBlock = block
	}
}

func main() {
	dir := "./4760K_to_5040K"
	files, _ := ioutil.ReadDir(dir)

	stat := newStatCollection()
	for _, fStat := range files {
		if fStat.IsDir() {
			continue
		}
		if !strings.HasPrefix(fStat.Name(), "metrics_to") {
			continue
		}
		blockstring := strings.Split(fStat.Name(), "_")[2]
		blnum, _ := strconv.Atoi(blockstring)
		dat, err := ioutil.ReadFile(fmt.Sprintf("%s/%s", dir, fStat.Name()))
		if err != nil {
			fmt.Printf("error: %v", err)
			os.Exit(1)
		}
		stat.collect(blnum, dat)
	}
	stat.chartMGasPerS()
	// First run
	//readFile("metrics_to_150000")
	// Second run
	//readFile("metrics_to_190000")
	// third run, non-optimized, on other computer
}

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"time"

	"github.com/golang/glog"
)

// Arg is struct for commandline arg.
type Arg struct {
	// 処理対象のファイル
	targetFile string

	// 分割カウントする際の分割数
	splitNum int

	// 同時実行するスレッド(の最大)数
	maxThreads int

	// ファイル読み込み用Bufferのサイズ
	buffersize int
}

var (
	arg Arg
)

func init() {
	// ヘルプメッセージを設定
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s\n", fmt.Sprintf("%s -f TARGETFILE [options] [glog options]", os.Args[0]))
		flag.PrintDefaults()
	}

	// loggerの初期設定
	_ = flag.Set("stderrthreshold", "INFO")
	_ = flag.Set("v", "0")

	// コマンドラインオプションの設定
	flag.StringVar(&arg.targetFile, "f", "", "(go-lc) Target File")
	flag.IntVar(&arg.splitNum, "s", 2, "(go-lc) Num of File split")
	flag.IntVar(&arg.maxThreads, "t", 2, "(go-lc) Max Num of Threads")
	flag.IntVar(&arg.buffersize, "b", 1024*1024, "(go-lc) Size of ReadBuffer")
}

func getFileSize(filename string) (int, error) {
	// 対象ファイルを開く
	fh, err := os.OpenFile(filename, 0, 0)
	if err != nil {
		return 0, err
	}
	defer fh.Close()

	// ファイル情報を取得
	fileinfo, err := fh.Stat()
	if err != nil {
		return 0, err
	}

	// ファイルのバイト数を取得して返す
	return int(fileinfo.Size()), nil
}

func getNumOfLines(filename string, splitNum int, maxThreads int, buffersize int) (int, error) {
	// ファイルサイズを取得
	fsize, err := getFileSize(filename)
	if err != nil {
		return 0, err
	}

	// loglevel = 1で情報表示
	glog.V(1).Infof("FileSize   : %10d byte", fsize)
	glog.V(1).Infof("Read buffer: %10d byte", buffersize)
	glog.V(1).Infof("Max Threads: %d", maxThreads)
	glog.V(1).Infof("Split Num  : %d", splitNum)

	// buffersizeの単位で何回読み込みができるかを算出。
	var readCountTotal int = int(math.Trunc(float64(fsize) / float64(buffersize)))

	// 余りがあった場合、読み込み回数に1を加算
	if fsize-(readCountTotal*buffersize) > 0 {
		readCountTotal++
	}

	// 終了待機用グループを初期化
	wg := &sync.WaitGroup{}

	// goroutineの同時実行数を制限するためのチャネル
	jc := make(chan interface{}, maxThreads)
	defer close(jc)

	// 各goroutineの行数カウント結果を受け取るチャネル
	// deferで解放していないのは、この関数の終了条件がcounterChの解放であるため
	// counterCh解放 -> 結果受信用goroutine終了 -> resultChに集計結果が入る -> return
	counterCh := make(chan int, maxThreads)

	// 結果受信用goroutineから集計結果を受信するためのチャネル
	resultCh := make(chan int)
	defer close(resultCh)

	// 結果受信用goroutineを起動
	// 終了条件はclose(counterCh)
	go func(counterCh <-chan int) {
		cAll := 0
		for c := range counterCh {
			cAll += c

			glog.V(2).Infof("[receiver] receive: %d\n", c)
		}

		resultCh <- cAll
	}(counterCh)

	// 行数カウントgoroutineに渡す読み込み開始位置(0は#1のgoroutineのため)
	var byteOffset int64 = 0

	// 行数カウントgoroutineを起動するためのループ
	for i := 0; i < splitNum; i++ {
		// countLinesInThread内で、何回buffer読み出しを行うか
		eachReadCount := int(math.Trunc(float64(readCountTotal+i) / float64(splitNum)))

		// goroutineの起動数配列を1つ埋める
		jc <- true

		// waitgroupを1つ増やす
		wg.Add(1)

		// 行数カウントgoroutineを起動
		go countWorker(filename, eachReadCount, byteOffset, buffersize, wg, jc, counterCh)

		// 読み込みオフセットを進める
		byteOffset += int64(eachReadCount * buffersize)
	}

	wg.Wait()
	close(counterCh)

	return <-resultCh, nil
}

func countWorker(filename string, eachReadCount int, byteOffset int64, buffersize int,
	wg *sync.WaitGroup, jc <-chan interface{}, counter chan<- int) {
	var c int = 0

	defer func() {
		// 無名関数はアウタースコープの変数にアクセスできるため問題ない
		counter <- c
		wg.Done()
		<-jc
	}()

	// loglevel=2で情報表示
	glog.V(2).Infof("[countWorker] start (offset: %d, read size: %d)\n", byteOffset, eachReadCount*buffersize)

	// 対象ファイルを再度開く
	// 元のファイルハンドラを使用するとSeekの読み出しカーソルがおかしくなるため
	f, err := os.OpenFile(filename, 0, 0)
	if err != nil {
		return
	}
	defer f.Close()

	// 指定された読み込み開始位置まで移動
	_, err = f.Seek(byteOffset, 0)
	if err != nil {
		return
	}

	// getNumOfCharsOnIoには次のように作成したbufioを渡すこともできます
	// 今回はio.Readerから読み出すデータのサイズを変更できるのでメリットが少ないと考え不使用
	// br := bufio.NewReaderSize(f, 1024*1024)

	c, err = getNumOfCharsOnIo(f, buffersize, eachReadCount)
	if err != nil {
		panic(err)
	}
}

// io.Readerからbuffersizeづつ読み出し、targetStrの出現数を数える処理をrepeatCount回繰り返します
func getNumOfCharsOnIo(r io.Reader, buffersize int, repeatCount int) (int, error) {
	// 読み込みバッファを初期化
	buf := make([]byte, buffersize)

	var c int = 0

	// 開始位置から、buffersizeづつバイト列を読み込んでbufに代入
	for j := 0; j < repeatCount; j++ {
		n, err := r.Read(buf)
		// 読み込みサイズが0だった場合
		if n == 0 {
			return c, err
		}

		// Readエラー時の処理
		if err != nil {
			return c, err
		}

		// Bufferの中身を走査するためのオフセット
		of := 0

		// Buffer内の改行を数える
		for {
			// https://ja.wikipedia.org/wiki/UTF-8
			// nでサイズを指定しているのは、bufを使いまわしているから
			// index := bytes.IndexRune(buf[of:n], rune('\n'))
			index := bytes.IndexByte(buf[of:n], '\n')
			if index == -1 {
				break
			}

			// (改行の)カウンタをインクリメント
			c++

			// 発見位置+1までオフセットを進める
			of += index + 1
		}
	}

	return c, nil
}

func main() {
	flag.Parse()

	glog.V(1).Infof("Start")

	// 処理時間算出用のタイマを開始
	startTime := time.Now()

	// 集計処理を実行
	numOfLines, _ := getNumOfLines(arg.targetFile, arg.splitNum, arg.maxThreads, arg.buffersize)

	// 処理時間を表示
	glog.V(1).Infof("End(%s)", time.Since(startTime))

	fmt.Printf("%d\n", numOfLines)
}

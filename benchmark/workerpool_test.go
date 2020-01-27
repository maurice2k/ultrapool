package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	cryptoRand "crypto/rand"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"

	wp_gammazero "github.com/gammazero/workerpool"
	"github.com/maurice2k/ultrapool"
	wp_fasthttp "github.com/maurice2k/ultrapool/benchmark/fasthttp"
	wp_ants "github.com/panjf2000/ants/v2"

	"testing"
)

var wg sync.WaitGroup

var aesKey = []byte("0123456789ABCDEF")
var oneKiloByte = []byte(strings.Repeat("a", 1024))
var eightKiloByte = []byte(strings.Repeat("a", 8192))

//var runs = []int{500}
var runs = []int{10, 100, 500, 1000}

func taskHandler(task ultrapool.Task) {
	//time.Sleep(time.Microsecond)
	//sha256.Sum256(oneKiloByte)
	encryptCBC(oneKiloByte, aesKey)
	//encryptCBC(eightKiloByte, aesKey)

	wg.Done()
}

func taskHandlerConn(conn net.Conn) error {
	//time.Sleep(time.Microsecond)
	//sha256.Sum256(oneKiloByte)
	encryptCBC(oneKiloByte, aesKey)
	//encryptCBC(eightKiloByte, aesKey)

	wg.Done()
	return nil
}

func BenchmarkGoRoutineWithoutWorkerpool(b *testing.B) {
	runtime.GC()

	b.ResetTimer()

	for _, parallelism := range runs {
		b.Run(fmt.Sprintf("%4d", parallelism), func(b *testing.B) {
			b.ReportAllocs()
			b.SetParallelism(parallelism)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					wg.Add(1)
					c := new(net.TCPConn)
					go taskHandler(c)
				}
			})
		})
	}

	wg.Wait()
}

func BenchmarkAntsWorkerpool(b *testing.B) {
	runtime.GC()

	wp, _ := wp_ants.NewPoolWithFunc(10000000, func(task interface{}) {
		taskHandler(task.(ultrapool.Task))
	}, wp_ants.WithPreAlloc(false))

	b.ResetTimer()

	for _, parallelism := range runs {
		b.Run(fmt.Sprintf("%4d", parallelism), func(b *testing.B) {
			b.ReportAllocs()
			b.SetParallelism(parallelism)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					wg.Add(1)
					c := new(net.TCPConn)
					wp.Invoke(c)
				}
			})
		})
	}

	wg.Wait()

	b.StopTimer()
	wp.Release()
}

func BenchmarkUltrapoolWorkerpool(b *testing.B) {
	runtime.GC()

	wp := ultrapool.NewWorkerPool(taskHandler)
	wp.SetIdleWorkerLifetime(time.Second)

	shards := runtime.GOMAXPROCS(0)
	wp.SetNumShards(shards)
	wp.Start()

	b.ResetTimer()

	for _, parallelism := range runs {
		b.Run(fmt.Sprintf("[%d]-%4d", shards, parallelism), func(b *testing.B) {
			b.ReportAllocs()
			b.SetParallelism(parallelism)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					wg.Add(1)
					c := new(net.TCPConn)
					wp.AddTask(c)
				}
			})
		})
	}

	wg.Wait()

	b.StopTimer()
	wp.Stop()
}

func BenchmarkFasthttpWorkerpool(b *testing.B) {
	runtime.GC()

	wp := &wp_fasthttp.WorkerPool{
		WorkerFunc:            taskHandlerConn,
		MaxWorkersCount:       10000000,
		LogAllErrors:          false,
		Logger:                nil,
		MaxIdleWorkerDuration: time.Second * 15,
	}
	wp.Start()

	b.ResetTimer()

	for _, parallelism := range runs {
		b.Run(fmt.Sprintf("%4d", parallelism), func(b *testing.B) {
			b.ReportAllocs()
			b.SetParallelism(parallelism)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					wg.Add(1)
					c := new(net.TCPConn)
					wp.Serve(c)
				}
			})
		})
	}

	wg.Wait()

	b.StopTimer()
	wp.Stop()
}

func BenchmarkGammazeroWorkerpool(b *testing.B) {
	runtime.GC()

	wp := wp_gammazero.New(10000000)

	b.ResetTimer()

	for _, parallelism := range runs {
		b.Run(fmt.Sprintf("%4d", parallelism), func(b *testing.B) {
			b.ReportAllocs()
			b.SetParallelism(parallelism)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					wg.Add(1)
					c := new(net.TCPConn)
					wp.Submit(func() {
						taskHandlerConn(c)
					})
				}
			})
		})
	}

	wg.Wait()

	b.StopTimer()
	wp.Stop()
}

// Encrypts given cipher text (prepended with the IV) with AES-128 or AES-256
// (depending on the length of the key)
func encryptCBC(plainText, key []byte) (cipherText []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	plainText = pad(aes.BlockSize, plainText)

	cipherText = make([]byte, aes.BlockSize+len(plainText))
	iv := cipherText[:aes.BlockSize]
	_, err = io.ReadFull(cryptoRand.Reader, iv)
	if err != nil {
		return nil, err
	}

	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(cipherText[aes.BlockSize:], plainText)

	return cipherText, nil
}

// Adds PKCS#7 padding (variable block length <= 255 bytes)
func pad(blockSize int, buf []byte) []byte {
	padLen := blockSize - (len(buf) % blockSize)
	padding := bytes.Repeat([]byte{byte(padLen)}, padLen)
	return append(buf, padding...)
}

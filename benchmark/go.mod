module github.com/maurice2k/ultrapool/benchmark

go 1.24

replace github.com/maurice2k/ultrapool/v2 => ../

require (
	github.com/Jeffail/tunny v0.1.4
	github.com/alitto/pond/v2 v2.7.1
	github.com/gammazero/workerpool v1.2.1
	github.com/maurice2k/ultrapool/v2 v2.0.0
	github.com/panjf2000/ants/v2 v2.12.0
)

require (
	github.com/gammazero/deque v1.2.1 // indirect
	golang.org/x/sync v0.11.0 // indirect
)

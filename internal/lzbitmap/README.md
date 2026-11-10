# LZBITMAP encoder

`Encode` wraps LZBITMAP level 2 (selector 0x702) for RIDIFF fork
reconstruction. The destination capacity affects encoding decisions;
RIDIFF uses exactly 65,536 bytes.

`lzbitmap.c` and `lzbitmap.h` are copied without modification from
https://github.com/retrowave3/apple-compressions at commit
6d97f1ab520821b4d326139fdf48b658f96ee5b9, under the MIT licence in `LICENSE`.

Round-trip tests use ipsw's existing lzfse-cgo decoder dependency. On
macOS, tests also compare the encoder with Apple's libcompression for
3,000 generated inputs at two destination capacities.

# `khata.aar` goes here

This directory holds the gomobile build of the Go engine. It is not checked in,
because it is a build artifact of the `core/` packages sitting one level up.

Build it from the repository root:

```sh
make aar
```

which runs:

```sh
gomobile bind -target=android -androidapi 26 \
  -javapkg=dev.khata.engine \
  -o android/app/libs/khata.aar \
  ./core/mobile
```

You need the Android NDK and `gomobile` on your PATH:

```sh
go install golang.org/x/mobile/cmd/gomobile@latest
gomobile init
```

The same `core/mobile` package is what the desktop CLI links against, so the
phone and the laptop cannot disagree about how a message is parsed.

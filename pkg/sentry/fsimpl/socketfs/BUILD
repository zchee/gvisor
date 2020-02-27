load("//tools:defs.bzl", "go_library")

licenses(["notice"])

go_library(
    name = "socketfs",
    srcs = ["socketfs.go"],
    visibility = ["//pkg/sentry:internal"],
    deps = [
        "//pkg/sentry/fsimpl/kernfs",
        "//pkg/sentry/vfs",
        "//pkg/syserror",
    ],
)

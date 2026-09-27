# shellcheck shell=bash
# setup_sqlite_cgo.sh — 为 CGO 构建提供 sqlite3.h（Windows/MinGW 本地开发）
#
# 背景：internal/application/repository/retriever/sqlite 依赖
# github.com/asg017/sqlite-vec-go-bindings/cgo，其 sqlite-vec.h 在 -DSQLITE_CORE 下
# #include "sqlite3.h"。Linux 构建镜像用 apt 安装 libsqlite3-dev 提供该头文件
# （见 docker/Dockerfile.app），Windows/MinGW 默认没有，导致：
#   ./sqlite-vec.h:7:10: fatal error: sqlite3.h: No such file or directory
#
# 本脚本生成一个 shim 头文件，转发到 mattn/go-sqlite3 自带的 sqlite3-binding.h
# （即 SQLite amalgamation 官方头文件，只是文件名不同），并通过 CGO_CPPFLAGS
# 把 shim 目录与 go-sqlite3 模块目录一起暴露给 CGo。
#
# 约束：不修改 Go module cache；shim 生成在仓库 .cache/ 下（.gitignore 已忽略 .*）。
#
# 用法（必须用 source 引入，才能导出 CGO_CPPFLAGS 到当前 shell）：
#   source scripts/setup_sqlite_cgo.sh

# 独立 source 时的兜底日志函数与项目根目录（dev.sh 已定义则沿用）
command -v log_info >/dev/null 2>&1 || log_info() { printf "[INFO] %s\n" "$1"; }
command -v log_warning >/dev/null 2>&1 || log_warning() { printf "[WARN] %s\n" "$1"; }
if [ -z "${PROJECT_ROOT:-}" ]; then
    PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fi

setup_sqlite_cgo() {
    # CGO 关闭或没有编译器时无需处理
    if [ "${CGO_ENABLED:-1}" = "0" ] || ! command -v gcc >/dev/null 2>&1; then
        return 0
    fi
    # 编译器已能找到 sqlite3.h（Linux libsqlite3-dev、vcpkg 等）时无需 shim
    if printf '#include <sqlite3.h>\n' | gcc -E -x c - >/dev/null 2>&1; then
        return 0
    fi
    if ! command -v go >/dev/null 2>&1; then
        return 0
    fi

    # 定位 go-sqlite3 模块目录（版本随 go.mod 变化，动态解析，不硬编码路径）
    local mod_dir
    mod_dir="$(go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3 2>/dev/null)"
    if [ -z "$mod_dir" ] || [ ! -f "$mod_dir/sqlite3-binding.h" ]; then
        log_warning "未找到 go-sqlite3 的 sqlite3-binding.h，sqlite-vec 可能编译失败；请先 go mod download"
        return 0
    fi

    # 生成/刷新 shim：sqlite-vec.h 引用 "sqlite3.h"，转发到 amalgamation 头文件
    local shim_dir="$PROJECT_ROOT/.cache/sqlite3-cgo-shim"
    mkdir -p "$shim_dir"
    cat > "$shim_dir/sqlite3.h" <<'EOF'
#ifndef WEKNORA_SQLITE3_CGO_SHIM_H
#define WEKNORA_SQLITE3_CGO_SHIM_H
/* 自动生成，请勿手动编辑：scripts/setup_sqlite_cgo.sh
 * sqlite-vec(-DSQLITE_CORE) 需要 "sqlite3.h"，转发到 mattn/go-sqlite3 自带的
 * SQLite amalgamation 头文件（同目录经 CGO_CPPFLAGS -I 提供）。 */
#include "sqlite3-binding.h"
#endif
EOF

    # Windows 下 gcc -I 对反斜杠路径解析不可靠，统一转成 C:/ 风格
    local inc_mod="$mod_dir" inc_shim="$shim_dir"
    if command -v cygpath >/dev/null 2>&1; then
        inc_mod="$(cygpath -m "$mod_dir")"
        inc_shim="$(cygpath -m "$shim_dir")"
    fi

    # 幂等注入，避免重复 source 时重复追加
    case " ${CGO_CPPFLAGS:-} " in
        *" -I$inc_shim "*) ;;
        *) export CGO_CPPFLAGS="-I$inc_shim -I$inc_mod${CGO_CPPFLAGS:+ $CGO_CPPFLAGS}" ;;
    esac
    log_info "已注入 CGO_CPPFLAGS sqlite3.h shim: $inc_shim -> go-sqlite3 amalgamation"
}

setup_sqlite_cgo

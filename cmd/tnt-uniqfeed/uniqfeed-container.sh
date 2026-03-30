#!/usr/bin/env bash

set -euo pipefail

usage() {
    cat <<'EOF'
Usage:
  ./uniqfeed-container.sh build
  ./uniqfeed-container.sh run <input> <output> <project_path> <metadata_dir>
  ./uniqfeed-container.sh shell [bash-args...]

Environment:
  FFMPEG_DIST    path to built FFmpeg dist directory
  UF_BASE_IMAGE  uniqFEED container image tag
                 default: uf_render_interface:ubuntu_22

Examples:
    FFMPEG_DIST=~/ELV/FFmpeg/dist ./uniqfeed-container.sh build
    FFMPEG_DIST=~/ELV/FFmpeg/dist ./uniqfeed-container.sh run input.mp4 output.mp4 /runtime/project /runtime/example_input
    FFMPEG_DIST=~/ELV/FFmpeg/dist ./uniqfeed-container.sh shell
EOF
}

add_pkg_config_mounts() {
    local mounts_var_name=$1
    local seen_var_name=$2
    local path_entry

    [[ -n "${PKG_CONFIG_PATH:-}" ]] || return 0

    IFS=':' read -r -a pkg_paths <<< "${PKG_CONFIG_PATH}"
    for path_entry in "${pkg_paths[@]}"; do
        [[ -n "${path_entry}" ]] || continue
        add_mount_if_needed "${mounts_var_name}" "${seen_var_name}" "$(expand_path "${path_entry}")" ro
    done
}

resolve_ffmpeg_dist() {
    if [[ -n "${FFMPEG_DIST:-}" ]]; then
        expand_path "${FFMPEG_DIST}"
        return 0
    fi

    if [[ -d "${repo_root}/../FFmpeg/dist" ]]; then
        printf '%s\n' "${repo_root}/../FFmpeg/dist"
        return 0
    fi

    echo "FFMPEG_DIST is not set and ../FFmpeg/dist was not found" >&2
    exit 1
}

expand_path() {
    local path=$1

    case "${path}" in
        ~)
            printf '%s\n' "${HOME}"
            ;;
        ~/*)
            printf '%s\n' "${HOME}/${path#~/}"
            ;;
        *)
            printf '%s\n' "${path}"
            ;;
    esac
}

add_mount_if_needed() {
    local -n mounts_ref=$1
    local -n seen_ref=$2
    local host_path=$3
    local mode=$4
    local mount_path

    [[ "${host_path}" = /* ]] || return 0
    [[ "${host_path}" = /runtime/* ]] && return 0

    if [[ -d "${host_path}" ]]; then
        mount_path=${host_path}
    else
        mount_path=$(dirname "${host_path}")
    fi

    [[ -d "${mount_path}" ]] || return 0

    if [[ -z "${seen_ref[${mount_path}]+x}" ]]; then
        mounts_ref+=( -v "${mount_path}:${mount_path}:${mode}" )
        seen_ref[${mount_path}]=1
    fi
}

if [[ $# -lt 1 ]]; then
    usage >&2
    exit 1
fi

cmd_dir=$(cd "$(dirname "$0")" && pwd)
repo_root=$(cd "${cmd_dir}/../.." && pwd)
compose_file="${cmd_dir}/docker-compose.uniqfeed.yml"
binary_path="${repo_root}/bin/tnt-uniqfeed"

base_image="${UF_BASE_IMAGE:-uf_render_interface:ubuntu_22}"
host_uid=$(id -u)
host_gid=$(id -g)
ffmpeg_dist=$(resolve_ffmpeg_dist)
passthrough_on_failure="${UF_RENDERLIB_PASSTHROUGH_ON_FAILURE:-1}"

compose_run_cmd=(docker compose -f "${compose_file}" run --rm -e UF_BASE_IMAGE="${base_image}" -e FFMPEG_DIST="${ffmpeg_dist}")

if [[ -n "${PKG_CONFIG_PATH:-}" ]]; then
    compose_run_cmd+=( -e PKG_CONFIG_PATH="${PKG_CONFIG_PATH}" )
fi

compose_run_cmd+=( -e UF_RENDERLIB_PASSTHROUGH_ON_FAILURE="${passthrough_on_failure}" )

ensure_binary_built() {
    local -a extra_mounts=()
    local -A seen_mounts=()

    if [[ -x "${binary_path}" ]]; then
        return 0
    fi

    echo "${binary_path} is missing; building tnt-uniqfeed before run" >&2

    add_mount_if_needed extra_mounts seen_mounts "${ffmpeg_dist}" ro
    add_pkg_config_mounts extra_mounts seen_mounts

    "${compose_run_cmd[@]}" "${extra_mounts[@]}" avpipe-tnt-uniqfeed ./build-in-container.sh
}

command_name=$1
shift

case "${command_name}" in
    build)
        declare -a extra_mounts=()
        declare -A seen_mounts=()

        add_mount_if_needed extra_mounts seen_mounts "${ffmpeg_dist}" ro
        add_pkg_config_mounts extra_mounts seen_mounts

        "${compose_run_cmd[@]}" "${extra_mounts[@]}" avpipe-tnt-uniqfeed ./build-in-container.sh
        ;;
    run)
        if [[ $# -ne 4 ]]; then
            echo "run requires 4 arguments" >&2
            usage >&2
            exit 1
        fi

        ensure_binary_built

        input_path=$(expand_path "$1")
        output_path=$(expand_path "$2")
        project_path=$(expand_path "$3")
        metadata_path=$(expand_path "$4")

        declare -a extra_mounts=()
        declare -A seen_mounts=()

        add_mount_if_needed extra_mounts seen_mounts "${ffmpeg_dist}" ro
        add_mount_if_needed extra_mounts seen_mounts "${input_path}" ro
        add_mount_if_needed extra_mounts seen_mounts "${output_path}" rw
        add_mount_if_needed extra_mounts seen_mounts "${project_path}" ro
        add_mount_if_needed extra_mounts seen_mounts "${metadata_path}" ro

        "${compose_run_cmd[@]}" --user "${host_uid}:${host_gid}" "${extra_mounts[@]}" avpipe-tnt-uniqfeed ./run-in-container.sh \
            "${input_path}" "${output_path}" "${project_path}" "${metadata_path}"
        ;;
    shell)
        declare -a extra_mounts=()
        declare -A seen_mounts=()
        shell_compose_cmd=(docker compose -f "${compose_file}" run --rm \
            -e UF_BASE_IMAGE="${base_image}" \
            -e FFMPEG_DIST="${ffmpeg_dist}" \
            -e UF_RENDERLIB_PASSTHROUGH_ON_FAILURE="${passthrough_on_failure}")

        if [[ -n "${PKG_CONFIG_PATH:-}" ]]; then
            shell_compose_cmd+=( -e PKG_CONFIG_PATH="${PKG_CONFIG_PATH}" )
        fi

        add_mount_if_needed extra_mounts seen_mounts "${ffmpeg_dist}" ro
        add_pkg_config_mounts extra_mounts seen_mounts

        exec "${shell_compose_cmd[@]}" \
            "${extra_mounts[@]}" \
            avpipe-tnt-uniqfeed "$@"
        ;;
    -h|--help|help)
        usage
        ;;
    *)
        echo "Unknown command: ${command_name}" >&2
        usage >&2
        exit 1
        ;;
esac
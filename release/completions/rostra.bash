# bash completion for rostra                               -*- shell-script -*-

__rostra_debug()
{
    if [[ -n ${BASH_COMP_DEBUG_FILE:-} ]]; then
        echo "$*" >> "${BASH_COMP_DEBUG_FILE}"
    fi
}

# Homebrew on Macs have version 1.3 of bash-completion which doesn't include
# _init_completion. This is a very minimal version of that function.
__rostra_init_completion()
{
    COMPREPLY=()
    _get_comp_words_by_ref "$@" cur prev words cword
}

__rostra_index_of_word()
{
    local w word=$1
    shift
    index=0
    for w in "$@"; do
        [[ $w = "$word" ]] && return
        index=$((index+1))
    done
    index=-1
}

__rostra_contains_word()
{
    local w word=$1; shift
    for w in "$@"; do
        [[ $w = "$word" ]] && return
    done
    return 1
}

__rostra_handle_go_custom_completion()
{
    __rostra_debug "${FUNCNAME[0]}: cur is ${cur}, words[*] is ${words[*]}, #words[@] is ${#words[@]}"

    local shellCompDirectiveError=1
    local shellCompDirectiveNoSpace=2
    local shellCompDirectiveNoFileComp=4
    local shellCompDirectiveFilterFileExt=8
    local shellCompDirectiveFilterDirs=16

    local out requestComp lastParam lastChar comp directive args

    # Prepare the command to request completions for the program.
    # Calling ${words[0]} instead of directly rostra allows handling aliases
    args=("${words[@]:1}")
    # Disable ActiveHelp which is not supported for bash completion v1
    requestComp="ROSTRA_ACTIVE_HELP=0 ${words[0]} __completeNoDesc ${args[*]}"

    lastParam=${words[$((${#words[@]}-1))]}
    lastChar=${lastParam:$((${#lastParam}-1)):1}
    __rostra_debug "${FUNCNAME[0]}: lastParam ${lastParam}, lastChar ${lastChar}"

    if [ -z "${cur}" ] && [ "${lastChar}" != "=" ]; then
        # If the last parameter is complete (there is a space following it)
        # We add an extra empty parameter so we can indicate this to the go method.
        __rostra_debug "${FUNCNAME[0]}: Adding extra empty parameter"
        requestComp="${requestComp} \"\""
    fi

    __rostra_debug "${FUNCNAME[0]}: calling ${requestComp}"
    # Use eval to handle any environment variables and such
    out=$(eval "${requestComp}" 2>/dev/null)

    # Extract the directive integer at the very end of the output following a colon (:)
    directive=${out##*:}
    # Remove the directive
    out=${out%:*}
    if [ "${directive}" = "${out}" ]; then
        # There is not directive specified
        directive=0
    fi
    __rostra_debug "${FUNCNAME[0]}: the completion directive is: ${directive}"
    __rostra_debug "${FUNCNAME[0]}: the completions are: ${out}"

    if [ $((directive & shellCompDirectiveError)) -ne 0 ]; then
        # Error code.  No completion.
        __rostra_debug "${FUNCNAME[0]}: received error from custom completion go code"
        return
    else
        if [ $((directive & shellCompDirectiveNoSpace)) -ne 0 ]; then
            if [[ $(type -t compopt) = "builtin" ]]; then
                __rostra_debug "${FUNCNAME[0]}: activating no space"
                compopt -o nospace
            fi
        fi
        if [ $((directive & shellCompDirectiveNoFileComp)) -ne 0 ]; then
            if [[ $(type -t compopt) = "builtin" ]]; then
                __rostra_debug "${FUNCNAME[0]}: activating no file completion"
                compopt +o default
            fi
        fi
    fi

    if [ $((directive & shellCompDirectiveFilterFileExt)) -ne 0 ]; then
        # File extension filtering
        local fullFilter filter filteringCmd
        # Do not use quotes around the $out variable or else newline
        # characters will be kept.
        for filter in ${out}; do
            fullFilter+="$filter|"
        done

        filteringCmd="_filedir $fullFilter"
        __rostra_debug "File filtering command: $filteringCmd"
        $filteringCmd
    elif [ $((directive & shellCompDirectiveFilterDirs)) -ne 0 ]; then
        # File completion for directories only
        local subdir
        # Use printf to strip any trailing newline
        subdir=$(printf "%s" "${out}")
        if [ -n "$subdir" ]; then
            __rostra_debug "Listing directories in $subdir"
            __rostra_handle_subdirs_in_dir_flag "$subdir"
        else
            __rostra_debug "Listing directories in ."
            _filedir -d
        fi
    else
        while IFS='' read -r comp; do
            COMPREPLY+=("$comp")
        done < <(compgen -W "${out}" -- "$cur")
    fi
}

__rostra_handle_reply()
{
    __rostra_debug "${FUNCNAME[0]}"
    local comp
    case $cur in
        -*)
            if [[ $(type -t compopt) = "builtin" ]]; then
                compopt -o nospace
            fi
            local allflags
            if [ ${#must_have_one_flag[@]} -ne 0 ]; then
                allflags=("${must_have_one_flag[@]}")
            else
                allflags=("${flags[*]} ${two_word_flags[*]}")
            fi
            while IFS='' read -r comp; do
                COMPREPLY+=("$comp")
            done < <(compgen -W "${allflags[*]}" -- "$cur")
            if [[ $(type -t compopt) = "builtin" ]]; then
                [[ "${COMPREPLY[0]}" == *= ]] || compopt +o nospace
            fi

            # complete after --flag=abc
            if [[ $cur == *=* ]]; then
                if [[ $(type -t compopt) = "builtin" ]]; then
                    compopt +o nospace
                fi

                local index flag
                flag="${cur%=*}"
                __rostra_index_of_word "${flag}" "${flags_with_completion[@]}"
                COMPREPLY=()
                if [[ ${index} -ge 0 ]]; then
                    PREFIX=""
                    cur="${cur#*=}"
                    ${flags_completion[${index}]}
                    if [ -n "${ZSH_VERSION:-}" ]; then
                        # zsh completion needs --flag= prefix
                        eval "COMPREPLY=( \"\${COMPREPLY[@]/#/${flag}=}\" )"
                    fi
                fi
            fi

            if [[ -z "${flag_parsing_disabled}" ]]; then
                # If flag parsing is enabled, we have completed the flags and can return.
                # If flag parsing is disabled, we may not know all (or any) of the flags, so we fallthrough
                # to possibly call handle_go_custom_completion.
                return 0;
            fi
            ;;
    esac

    # check if we are handling a flag with special work handling
    local index
    __rostra_index_of_word "${prev}" "${flags_with_completion[@]}"
    if [[ ${index} -ge 0 ]]; then
        ${flags_completion[${index}]}
        return
    fi

    # we are parsing a flag and don't have a special handler, no completion
    if [[ ${cur} != "${words[cword]}" ]]; then
        return
    fi

    local completions
    completions=("${commands[@]}")
    if [[ ${#must_have_one_noun[@]} -ne 0 ]]; then
        completions+=("${must_have_one_noun[@]}")
    elif [[ -n "${has_completion_function}" ]]; then
        # if a go completion function is provided, defer to that function
        __rostra_handle_go_custom_completion
    fi
    if [[ ${#must_have_one_flag[@]} -ne 0 ]]; then
        completions+=("${must_have_one_flag[@]}")
    fi
    while IFS='' read -r comp; do
        COMPREPLY+=("$comp")
    done < <(compgen -W "${completions[*]}" -- "$cur")

    if [[ ${#COMPREPLY[@]} -eq 0 && ${#noun_aliases[@]} -gt 0 && ${#must_have_one_noun[@]} -ne 0 ]]; then
        while IFS='' read -r comp; do
            COMPREPLY+=("$comp")
        done < <(compgen -W "${noun_aliases[*]}" -- "$cur")
    fi

    if [[ ${#COMPREPLY[@]} -eq 0 ]]; then
        if declare -F __rostra_custom_func >/dev/null; then
            # try command name qualified custom func
            __rostra_custom_func
        else
            # otherwise fall back to unqualified for compatibility
            declare -F __custom_func >/dev/null && __custom_func
        fi
    fi

    # available in bash-completion >= 2, not always present on macOS
    if declare -F __ltrim_colon_completions >/dev/null; then
        __ltrim_colon_completions "$cur"
    fi

    # If there is only 1 completion and it is a flag with an = it will be completed
    # but we don't want a space after the =
    if [[ "${#COMPREPLY[@]}" -eq "1" ]] && [[ $(type -t compopt) = "builtin" ]] && [[ "${COMPREPLY[0]}" == --*= ]]; then
       compopt -o nospace
    fi
}

# The arguments should be in the form "ext1|ext2|extn"
__rostra_handle_filename_extension_flag()
{
    local ext="$1"
    _filedir "@(${ext})"
}

__rostra_handle_subdirs_in_dir_flag()
{
    local dir="$1"
    pushd "${dir}" >/dev/null 2>&1 && _filedir -d && popd >/dev/null 2>&1 || return
}

__rostra_handle_flag()
{
    __rostra_debug "${FUNCNAME[0]}: c is $c words[c] is ${words[c]}"

    # if a command required a flag, and we found it, unset must_have_one_flag()
    local flagname=${words[c]}
    local flagvalue=""
    # if the word contained an =
    if [[ ${words[c]} == *"="* ]]; then
        flagvalue=${flagname#*=} # take in as flagvalue after the =
        flagname=${flagname%=*} # strip everything after the =
        flagname="${flagname}=" # but put the = back
    fi
    __rostra_debug "${FUNCNAME[0]}: looking for ${flagname}"
    if __rostra_contains_word "${flagname}" "${must_have_one_flag[@]}"; then
        must_have_one_flag=()
    fi

    # if you set a flag which only applies to this command, don't show subcommands
    if __rostra_contains_word "${flagname}" "${local_nonpersistent_flags[@]}"; then
      commands=()
    fi

    # keep flag value with flagname as flaghash
    # flaghash variable is an associative array which is only supported in bash > 3.
    if [[ -z "${BASH_VERSION:-}" || "${BASH_VERSINFO[0]:-}" -gt 3 ]]; then
        if [ -n "${flagvalue}" ] ; then
            flaghash[${flagname}]=${flagvalue}
        elif [ -n "${words[ $((c+1)) ]}" ] ; then
            flaghash[${flagname}]=${words[ $((c+1)) ]}
        else
            flaghash[${flagname}]="true" # pad "true" for bool flag
        fi
    fi

    # skip the argument to a two word flag
    if [[ ${words[c]} != *"="* ]] && __rostra_contains_word "${words[c]}" "${two_word_flags[@]}"; then
        __rostra_debug "${FUNCNAME[0]}: found a flag ${words[c]}, skip the next argument"
        c=$((c+1))
        # if we are looking for a flags value, don't show commands
        if [[ $c -eq $cword ]]; then
            commands=()
        fi
    fi

    c=$((c+1))

}

__rostra_handle_noun()
{
    __rostra_debug "${FUNCNAME[0]}: c is $c words[c] is ${words[c]}"

    if __rostra_contains_word "${words[c]}" "${must_have_one_noun[@]}"; then
        must_have_one_noun=()
    elif __rostra_contains_word "${words[c]}" "${noun_aliases[@]}"; then
        must_have_one_noun=()
    fi

    nouns+=("${words[c]}")
    c=$((c+1))
}

__rostra_handle_command()
{
    __rostra_debug "${FUNCNAME[0]}: c is $c words[c] is ${words[c]}"

    local next_command
    if [[ -n ${last_command} ]]; then
        next_command="_${last_command}_${words[c]//:/__}"
    else
        if [[ $c -eq 0 ]]; then
            next_command="_rostra_root_command"
        else
            next_command="_${words[c]//:/__}"
        fi
    fi
    c=$((c+1))
    __rostra_debug "${FUNCNAME[0]}: looking for ${next_command}"
    declare -F "$next_command" >/dev/null && $next_command
}

__rostra_handle_word()
{
    if [[ $c -ge $cword ]]; then
        __rostra_handle_reply
        return
    fi
    __rostra_debug "${FUNCNAME[0]}: c is $c words[c] is ${words[c]}"
    if [[ "${words[c]}" == -* ]]; then
        __rostra_handle_flag
    elif __rostra_contains_word "${words[c]}" "${commands[@]}"; then
        __rostra_handle_command
    elif [[ $c -eq 0 ]]; then
        __rostra_handle_command
    elif __rostra_contains_word "${words[c]}" "${command_aliases[@]}"; then
        # aliashash variable is an associative array which is only supported in bash > 3.
        if [[ -z "${BASH_VERSION:-}" || "${BASH_VERSINFO[0]:-}" -gt 3 ]]; then
            words[c]=${aliashash[${words[c]}]}
            __rostra_handle_command
        else
            __rostra_handle_noun
        fi
    else
        __rostra_handle_noun
    fi
    __rostra_handle_word
}

_rostra_api()
{
    last_command="rostra_api"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flag_parsing_disabled=1
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    has_completion_function=1
    noun_aliases=()
}

_rostra_check()
{
    last_command="rostra_check"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_format()
{
    last_command="rostra_format"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--write")
    flags+=("-w")
    local_nonpersistent_flags+=("--write")
    local_nonpersistent_flags+=("-w")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_generate_ech-keypair()
{
    last_command="rostra_generate_ech-keypair"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_generate_rand()
{
    last_command="rostra_generate_rand"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--base64")
    local_nonpersistent_flags+=("--base64")
    flags+=("--hex")
    local_nonpersistent_flags+=("--hex")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_generate_reality-keypair()
{
    last_command="rostra_generate_reality-keypair"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_generate_tailcat-keypair()
{
    last_command="rostra_generate_tailcat-keypair"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_generate_tls-keypair()
{
    last_command="rostra_generate_tls-keypair"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--months=")
    two_word_flags+=("--months")
    two_word_flags+=("-m")
    local_nonpersistent_flags+=("--months")
    local_nonpersistent_flags+=("--months=")
    local_nonpersistent_flags+=("-m")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_generate_uuid()
{
    last_command="rostra_generate_uuid"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_generate_vapid-keypair()
{
    last_command="rostra_generate_vapid-keypair"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_generate_wg-keypair()
{
    last_command="rostra_generate_wg-keypair"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_generate()
{
    last_command="rostra_generate"

    command_aliases=()

    commands=()
    commands+=("ech-keypair")
    commands+=("rand")
    commands+=("reality-keypair")
    commands+=("tailcat-keypair")
    commands+=("tls-keypair")
    commands+=("uuid")
    commands+=("vapid-keypair")
    commands+=("wg-keypair")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_geoip_export()
{
    last_command="rostra_geoip_export"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--output=")
    two_word_flags+=("--output")
    two_word_flags+=("-o")
    local_nonpersistent_flags+=("--output")
    local_nonpersistent_flags+=("--output=")
    local_nonpersistent_flags+=("-o")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")
    flags+=("--file=")
    two_word_flags+=("--file")
    two_word_flags+=("-f")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_geoip_list()
{
    last_command="rostra_geoip_list"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")
    flags+=("--file=")
    two_word_flags+=("--file")
    two_word_flags+=("-f")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_geoip_lookup()
{
    last_command="rostra_geoip_lookup"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")
    flags+=("--file=")
    two_word_flags+=("--file")
    two_word_flags+=("-f")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_geoip()
{
    last_command="rostra_geoip"

    command_aliases=()

    commands=()
    commands+=("export")
    commands+=("list")
    commands+=("lookup")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--file=")
    two_word_flags+=("--file")
    two_word_flags+=("-f")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_geosite_export()
{
    last_command="rostra_geosite_export"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--output=")
    two_word_flags+=("--output")
    two_word_flags+=("-o")
    local_nonpersistent_flags+=("--output")
    local_nonpersistent_flags+=("--output=")
    local_nonpersistent_flags+=("-o")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")
    flags+=("--file=")
    two_word_flags+=("--file")
    two_word_flags+=("-f")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_geosite_list()
{
    last_command="rostra_geosite_list"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")
    flags+=("--file=")
    two_word_flags+=("--file")
    two_word_flags+=("-f")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_geosite_lookup()
{
    last_command="rostra_geosite_lookup"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")
    flags+=("--file=")
    two_word_flags+=("--file")
    two_word_flags+=("-f")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_geosite()
{
    last_command="rostra_geosite"

    command_aliases=()

    commands=()
    commands+=("export")
    commands+=("list")
    commands+=("lookup")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--file=")
    two_word_flags+=("--file")
    two_word_flags+=("-f")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_merge()
{
    last_command="rostra_merge"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_rule-set_compile()
{
    last_command="rostra_rule-set_compile"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--output=")
    two_word_flags+=("--output")
    two_word_flags+=("-o")
    local_nonpersistent_flags+=("--output")
    local_nonpersistent_flags+=("--output=")
    local_nonpersistent_flags+=("-o")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_rule-set_convert()
{
    last_command="rostra_rule-set_convert"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--output=")
    two_word_flags+=("--output")
    two_word_flags+=("-o")
    local_nonpersistent_flags+=("--output")
    local_nonpersistent_flags+=("--output=")
    local_nonpersistent_flags+=("-o")
    flags+=("--type=")
    two_word_flags+=("--type")
    two_word_flags+=("-t")
    local_nonpersistent_flags+=("--type")
    local_nonpersistent_flags+=("--type=")
    local_nonpersistent_flags+=("-t")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_rule-set_decompile()
{
    last_command="rostra_rule-set_decompile"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--output=")
    two_word_flags+=("--output")
    two_word_flags+=("-o")
    local_nonpersistent_flags+=("--output")
    local_nonpersistent_flags+=("--output=")
    local_nonpersistent_flags+=("-o")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_rule-set_format()
{
    last_command="rostra_rule-set_format"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--write")
    flags+=("-w")
    local_nonpersistent_flags+=("--write")
    local_nonpersistent_flags+=("-w")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_rule-set_match()
{
    last_command="rostra_rule-set_match"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--format=")
    two_word_flags+=("--format")
    two_word_flags+=("-f")
    local_nonpersistent_flags+=("--format")
    local_nonpersistent_flags+=("--format=")
    local_nonpersistent_flags+=("-f")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_rule-set_merge()
{
    last_command="rostra_rule-set_merge"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    local_nonpersistent_flags+=("--config")
    local_nonpersistent_flags+=("--config=")
    local_nonpersistent_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    local_nonpersistent_flags+=("--config-directory")
    local_nonpersistent_flags+=("--config-directory=")
    local_nonpersistent_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_rule-set_upgrade()
{
    last_command="rostra_rule-set_upgrade"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--write")
    flags+=("-w")
    local_nonpersistent_flags+=("--write")
    local_nonpersistent_flags+=("-w")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_rule-set()
{
    last_command="rostra_rule-set"

    command_aliases=()

    commands=()
    commands+=("compile")
    commands+=("convert")
    commands+=("decompile")
    commands+=("format")
    commands+=("match")
    commands+=("merge")
    commands+=("upgrade")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_run()
{
    last_command="rostra_run"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_schema()
{
    last_command="rostra_schema"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--output=")
    two_word_flags+=("--output")
    two_word_flags+=("-o")
    local_nonpersistent_flags+=("--output")
    local_nonpersistent_flags+=("--output=")
    local_nonpersistent_flags+=("-o")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_tools_connect()
{
    last_command="rostra_tools_connect"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--network=")
    two_word_flags+=("--network")
    two_word_flags+=("-n")
    local_nonpersistent_flags+=("--network")
    local_nonpersistent_flags+=("--network=")
    local_nonpersistent_flags+=("-n")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")
    flags+=("--outbound=")
    two_word_flags+=("--outbound")
    two_word_flags+=("-o")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_tools_fetch()
{
    last_command="rostra_tools_fetch"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")
    flags+=("--outbound=")
    two_word_flags+=("--outbound")
    two_word_flags+=("-o")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_tools_networkquality()
{
    last_command="rostra_tools_networkquality"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config-url=")
    two_word_flags+=("--config-url")
    local_nonpersistent_flags+=("--config-url")
    local_nonpersistent_flags+=("--config-url=")
    flags+=("--http3")
    local_nonpersistent_flags+=("--http3")
    flags+=("--max-runtime=")
    two_word_flags+=("--max-runtime")
    local_nonpersistent_flags+=("--max-runtime")
    local_nonpersistent_flags+=("--max-runtime=")
    flags+=("--serial")
    local_nonpersistent_flags+=("--serial")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")
    flags+=("--outbound=")
    two_word_flags+=("--outbound")
    two_word_flags+=("-o")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_tools_stun()
{
    last_command="rostra_tools_stun"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--server=")
    two_word_flags+=("--server")
    two_word_flags+=("-s")
    local_nonpersistent_flags+=("--server")
    local_nonpersistent_flags+=("--server=")
    local_nonpersistent_flags+=("-s")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")
    flags+=("--outbound=")
    two_word_flags+=("--outbound")
    two_word_flags+=("-o")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_tools_synctime()
{
    last_command="rostra_tools_synctime"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--format=")
    two_word_flags+=("--format")
    two_word_flags+=("-f")
    local_nonpersistent_flags+=("--format")
    local_nonpersistent_flags+=("--format=")
    local_nonpersistent_flags+=("-f")
    flags+=("--server=")
    two_word_flags+=("--server")
    two_word_flags+=("-s")
    local_nonpersistent_flags+=("--server")
    local_nonpersistent_flags+=("--server=")
    local_nonpersistent_flags+=("-s")
    flags+=("--write")
    flags+=("-w")
    local_nonpersistent_flags+=("--write")
    local_nonpersistent_flags+=("-w")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")
    flags+=("--outbound=")
    two_word_flags+=("--outbound")
    two_word_flags+=("-o")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_tools()
{
    last_command="rostra_tools"

    command_aliases=()

    commands=()
    commands+=("connect")
    commands+=("fetch")
    commands+=("networkquality")
    commands+=("stun")
    commands+=("synctime")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--outbound=")
    two_word_flags+=("--outbound")
    two_word_flags+=("-o")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_version()
{
    last_command="rostra_version"

    command_aliases=()

    commands=()

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--name")
    flags+=("-n")
    local_nonpersistent_flags+=("--name")
    local_nonpersistent_flags+=("-n")
    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

_rostra_root_command()
{
    last_command="rostra"

    command_aliases=()

    commands=()
    commands+=("api")
    commands+=("check")
    commands+=("format")
    commands+=("generate")
    commands+=("geoip")
    commands+=("geosite")
    commands+=("merge")
    commands+=("rule-set")
    commands+=("run")
    commands+=("schema")
    commands+=("tools")
    commands+=("version")

    flags=()
    two_word_flags=()
    local_nonpersistent_flags=()
    flags_with_completion=()
    flags_completion=()

    flags+=("--config=")
    two_word_flags+=("--config")
    two_word_flags+=("-c")
    flags+=("--config-directory=")
    two_word_flags+=("--config-directory")
    two_word_flags+=("-C")
    flags+=("--directory=")
    two_word_flags+=("--directory")
    two_word_flags+=("-D")
    flags+=("--disable-color")

    must_have_one_flag=()
    must_have_one_noun=()
    noun_aliases=()
}

__start_rostra()
{
    local cur prev words cword split
    declare -A flaghash 2>/dev/null || :
    declare -A aliashash 2>/dev/null || :
    if declare -F _init_completion >/dev/null 2>&1; then
        _init_completion -s || return
    else
        __rostra_init_completion -n "=" || return
    fi

    local c=0
    local flag_parsing_disabled=
    local flags=()
    local two_word_flags=()
    local local_nonpersistent_flags=()
    local flags_with_completion=()
    local flags_completion=()
    local commands=("rostra")
    local command_aliases=()
    local must_have_one_flag=()
    local must_have_one_noun=()
    local has_completion_function=""
    local last_command=""
    local nouns=()
    local noun_aliases=()

    __rostra_handle_word
}

if [[ $(type -t compopt) = "builtin" ]]; then
    complete -o default -F __start_rostra rostra
else
    complete -o default -o nospace -F __start_rostra rostra
fi

# ex: ts=4 sw=4 et filetype=sh

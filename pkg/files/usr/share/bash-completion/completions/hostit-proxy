# bash completion for hostit-proxy, answered by the CLI itself: the binary prints the
# candidates for the current word when called with --generate-bash-completion.
_hostit_proxy_completion() {
  if [[ "${COMP_WORDS[0]}" != "source" ]]; then
    local cur opts
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    if [[ "$cur" == -* ]]; then
      opts=$("${COMP_WORDS[@]:0:$COMP_CWORD}" "$cur" --generate-bash-completion 2>/dev/null)
    else
      opts=$("${COMP_WORDS[@]:0:$COMP_CWORD}" --generate-bash-completion 2>/dev/null)
    fi
    COMPREPLY=($(compgen -W "$opts" -- "$cur"))
    return 0
  fi
}
complete -o bashdefault -o default -o nospace -F _hostit_proxy_completion hostit-proxy

#!/usr/bin/env bash
#
# controller-contract.sh — the POSIX half of the two structured contracts a
# supervised driver stage shares with the run that invokes it.
#
# THE FIRST CONTRACT: THE STRUCTURED CONTROLLER RESULT.
#
# A supervised task states what happened to it in a structured document, and the
# run adjudicates that statement instead of the exit status the task's process
# happened to leave behind (internal/unattended/controller.go). driver.sh is the
# executable a compiled delivery run invokes for every stage, and until this file
# existed it stated nothing: its authoritative stage state reached the run as a
# shell exit status, which is the residue the whole contract exists to stop
# trusting. Four pilot failures came out of that residue — a stage exiting
# non-zero for a condition that was correct, a wrapper exiting zero over work
# that had been cut off, an authentication refusal retried as an ordinary
# command failure, and a deadline reported as success.
#
# It is a PRODUCER, exactly as corsolv/powershell/CorsolvControllerResult.psm1
# is on the Windows host, and for the same reason: there is no disposition, no
# retry policy and no gate verdict here, because all three are the run's to
# decide and a second opinion in a second language is a second authority that
# can disagree. What a stage does here is SAY what happened; what the run does
# about it stays one function in one language.
#
# The vocabulary is not hard-coded. It is read from
# controller-result.contract.json — the same document the Go consumer's tests
# and the PowerShell producer's tests are checked against — so a third
# implementation of one wire format cannot drift away from the other two
# quietly.
#
# THE SECOND CONTRACT: THE PROGRESSION DECISION.
#
# Whether a packet may progress is QA-001's answer, taken from gate evidence
# bound to a revision, and the run publishes it in the heartbeat it writes on
# every state change. The driver READS that decision; it never derives one. A
# delivery projection that claimed a met completion gate the run's own gates
# refused would be the reassuring account of two, and the one a person reads.

# cr_contract prints the shared contract document this driver is checked
# against.
#
# It resolves beside this file rather than from a configured root: the contract
# and the code that reads it ship together, and a copy that read a contract from
# somewhere else would be checked against a vocabulary it was not written for.
cr_contract() {
  if [ -n "${CORSOLV_CONTROLLER_CONTRACT:-}" ]; then
    printf '%s' "$CORSOLV_CONTROLLER_CONTRACT"
    return 0
  fi
  printf '%s/../powershell/controller-result.contract.json' "$(dirname "${BASH_SOURCE[0]}")"
}

# cr_declared_states prints the declared controller states, one per line.
cr_declared_states() {
  local contract
  contract="$(cr_contract)"
  if [ ! -f "$contract" ]; then
    printf 'controller-contract: the shared contract is missing at %s\n' "$contract" >&2
    return 1
  fi
  jq -r '.states[]' < "$contract"
}

# cr_state_is_declared reports whether a state is one the run can adjudicate.
cr_state_is_declared() {
  local want states
  want="$(printf '%s' "${1:-}" | tr -d '[:space:]' | tr '[:lower:]' '[:upper:]')"
  [ -n "$want" ] || return 1
  states="$(cr_declared_states)" || return 1
  grep -qxF -- "$want" <<< "$states"
}

# cr_result_path prints where this task is expected to state what happened to
# it, which is the path the run exported and nothing else. A task that was not
# started as a supervised task has nowhere to state anything, and says so by
# printing nothing.
cr_result_path() { printf '%s' "${GC_UNATTENDED_RESULT_PATH:-}"; }

# cr_supervised reports whether this task was started as a supervised one.
cr_supervised() { [ -n "${GC_UNATTENDED_RESULT_PATH:-}" ]; }

# cr_state_dir prints the run's own state directory, which is where the run
# publishes what it knows about itself. Empty when this task is not part of a
# run.
cr_state_dir() { printf '%s' "${GC_UNATTENDED_STATE_DIR:-}"; }

# cr_clear removes any document already sitting at the result path.
#
# It runs before the work rather than after it, so a stage that is killed
# part-way through leaves an ABSENCE — which fails safe — rather than an earlier
# statement that would be adjudicated as this attempt's. The run clears the path
# for its own attempts too; this is the same guarantee for a stage that writes
# more than once in its own lifetime.
cr_clear() {
  local path="${1:-${GC_UNATTENDED_RESULT_PATH:-}}"
  [ -n "$path" ] || return 0
  rm -f "$path"
}

# cr_document_is_usable reports whether a document is one the run could
# adjudicate.
#
# It asks exactly the question the consumer asks, and it is asked here so a
# stage discovers it has produced an unusable result while it is still running
# rather than by being failed for silence afterwards.
cr_document_is_usable() {
  local json="${1:-}" state
  [ -n "${json//[[:space:]]/}" ] || return 1
  state="$(jq -r 'if type == "object" and (.state | type) == "string" then .state else empty end' <<< "$json" 2>/dev/null)" || return 1
  cr_state_is_declared "$state"
}

# cr_write states what happened to this stage, where the run will read it.
#
#   cr_write --state COMPLETE|CONTINUE|FAILED|HUMAN_BLOCKED
#            [--reason <terminal reason>] [--subtype <runtime subtype>]
#            [--detail <text>] [--turns <n>] [--error] [--path <file>]
#
# The document is validated before it is written and written whole, through a
# temporary file and a rename: a half-written result is exactly the unusable
# document the consumer fails safe on, and producing one from this side would be
# self-inflicted. Only fields carrying a value are emitted, because an absent
# field and a zero-valued one read the same to the consumer and the zero value
# is noise in a document a person reads at three in the morning.
cr_write() {
  local state='' reason='' subtype='' detail='' turns='0' iserror='false' path=''
  while [ $# -gt 0 ]; do
    case "$1" in
      --state)   state="${2:-}"; shift 2 ;;
      --reason)  reason="${2:-}"; shift 2 ;;
      --subtype) subtype="${2:-}"; shift 2 ;;
      --detail)  detail="${2:-}"; shift 2 ;;
      --turns)   turns="${2:-0}"; shift 2 ;;
      --path)    path="${2:-}"; shift 2 ;;
      --error)   iserror='true'; shift ;;
      *) printf 'cr_write: unknown option %s\n' "$1" >&2; return 2 ;;
    esac
  done

  [ -n "$path" ] || path="$(cr_result_path)"
  if [ -z "$path" ]; then
    printf 'cr_write: %s is not set, so this task has nowhere to state its outcome\n' \
      'GC_UNATTENDED_RESULT_PATH' >&2
    return 1
  fi

  state="$(printf '%s' "$state" | tr -d '[:space:]' | tr '[:lower:]' '[:upper:]')"
  if ! cr_state_is_declared "$state"; then
    printf 'cr_write: refusing to write a controller result the run could not adjudicate: state %s\n' \
      "${state:-unset}" >&2
    return 1
  fi
  reason="$(printf '%s' "$reason" | tr -d '[:space:]' | tr '[:upper:]' '[:lower:]')"
  subtype="$(printf '%s' "$subtype" | tr -d '[:space:]' | tr '[:upper:]' '[:lower:]')"
  case "$turns" in
    ''|*[!0-9]*) turns='0' ;;
  esac

  local json
  json="$(jq -cn \
    --arg state "$state" --arg reason "$reason" --arg subtype "$subtype" \
    --arg detail "$detail" --argjson turns "$turns" --argjson iserror "$iserror" '
      {state: $state}
      + (if $reason  != "" then {terminal_reason: $reason} else {} end)
      + (if $subtype != "" then {subtype: $subtype} else {} end)
      + (if $iserror       then {is_error: true} else {} end)
      + (if $detail  != "" then {detail: $detail} else {} end)
      + (if $turns > 0     then {num_turns: $turns} else {} end)
    ')" || return 1

  if ! cr_document_is_usable "$json"; then
    printf 'cr_write: refusing to write a controller result the run could not adjudicate: %s\n' "$json" >&2
    return 1
  fi

  mkdir -p "$(dirname "$path")" || return 1
  local tmp="$path.tmp.$$"
  printf '%s\n' "$json" > "$tmp" && mv -f "$tmp" "$path"
}

# cr_reason_for_output reads a captured tool output and prints the contract's
# terminal reason for it, or nothing when the contract names none.
#
# Naming the reason is the PRODUCER'S job and not a second opinion about the
# disposition: the run decides what to do about an authentication refusal, and
# only the stage that ran the tool can see that the refusal happened at all. The
# stage captures its tools' output into evidence files rather than onto its own
# stdout, so a signature the run would have recognized in the text never reaches
# the run's classifier — which is how an expired credential was retried as an
# ordinary command failure.
#
# Order matters: a public-key refusal names a permission and IS an
# authentication failure, so authentication is asked first.
cr_reason_for_output() {
  local file="${1:-}"
  [ -n "$file" ] && [ -f "$file" ] || return 0
  if grep -Eqi 'authentication failed|bad credentials|not logged in|could not read (Username|Password)|terminal prompts disabled|permission denied \(publickey\)|(token|credential)[a-z]* [^.]*(expired|invalid|revoked)|gh auth login|HTTP 401' "$file"; then
    printf 'authentication_failed'
    return 0
  fi
  if grep -Eqi 'HTTP 403|forbidden|not authorized|resource not accessible|must have admin rights|permission to .* denied' "$file"; then
    printf 'permission_denied'
    return 0
  fi
  if grep -Eqi 'rate limit|HTTP 429|abuse detection' "$file"; then
    printf 'rate_limited'
    return 0
  fi
  if grep -Eqi 'timed out|timeout|connection (reset|refused)|could not resolve host|network is unreachable|TLS handshake|HTTP 50[234]|service unavailable|temporarily unavailable' "$file"; then
    printf 'network_timeout'
    return 0
  fi
  return 0
}

# cr_progression_refusal prints why the run refuses to let this packet progress,
# and prints nothing when the run permits it.
#
# The decision is READ, never derived. It is the same ProgressionDecision the
# run's completion event carries and the same one the run publisher applies as a
# ceiling to its own projection (internal/unattended/publish.go), so the driver's
# delivery projection and the run's cannot tell a reader two different stories
# about the same gates.
#
# A heartbeat that does not exist is not a refusal: a stage run outside a
# supervised run has no run to be governed by. A heartbeat that exists and
# carries no decision IS a refusal, because a decision nobody recorded is not a
# decision that permitted anything.
cr_progression_refusal() {
  local heartbeat="${1:-}"
  [ -n "$heartbeat" ] && [ -f "$heartbeat" ] || return 0

  local decided
  decided="$(jq -r 'if (.qa | type) == "object" then "yes" else "no" end' < "$heartbeat" 2>/dev/null)" || decided='no'
  if [ "$decided" != 'yes' ]; then
    printf 'the run recorded no progression decision in %s, so nothing is known about whether this packet may progress' \
      "$heartbeat"
    return 0
  fi

  jq -r '
    if (.qa.allowed == true) then empty
    else
      ((.qa.blocking // []) | map("\(.gateId) (\(.reason))") | join(", ")) as $blocking
      | "the run refuses progression for this packet at risk \(.qa.risk // "unknown")"
        + (if $blocking != "" then ": " + $blocking else "" end)
    end' < "$heartbeat" 2>/dev/null
}

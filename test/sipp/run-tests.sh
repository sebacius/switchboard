#!/bin/bash
#
# Switchboard SIP Test Suite
#
# Usage:
#   ./run-tests.sh <signaling_ip> [test]
#   ./run-tests.sh 192.168.50.181           # Run all tests
#   ./run-tests.sh 192.168.50.181 register  # Only registration
#   ./run-tests.sh 192.168.50.181 calls     # Only calls (assumes registered)
#

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'
BOLD='\033[1m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCENARIOS_DIR="$SCRIPT_DIR/scenarios"
RESULTS_DIR="$SCRIPT_DIR/results"

if [[ $# -lt 1 ]]; then
    echo "Usage: $0 <signaling_ip> [test]"
    echo ""
    echo "Tests: register, calls, parking, trunk, trunk-reject, trunk-did, all (default)"
    echo ""
    echo "Examples:"
    echo "  $0 192.168.50.181              # Run all"
    echo "  $0 192.168.50.181 register     # Only registration"
    echo "  $0 192.168.50.181 calls        # Only calls"
    echo "  $0 192.168.50.181 parking      # Only parking tests"
    echo "  $0 192.168.50.181 trunk-reject # Ingress gate: unknown source -> 403"
    echo "  $0 192.168.50.181 trunk-did    # Ingress gate: trunk DID routing (needs peer config)"
    exit 1
fi

TARGET="$1"
TEST="${2:-all}"
SIP_PORT=5060

# Check for sipp
if ! command -v sipp &> /dev/null; then
    echo -e "${RED}Error: sipp not found${NC}"
    exit 1
fi

# Setup
mkdir -p "$RESULTS_DIR"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
RUN_DIR="$RESULTS_DIR/$TIMESTAMP"
mkdir -p "$RUN_DIR"

echo ""
echo -e "${BOLD}══════════════════════════════════════════════════════════════${NC}"
echo -e "${BOLD}  SWITCHBOARD SIP TEST SUITE${NC}"
echo -e "${BOLD}══════════════════════════════════════════════════════════════${NC}"
echo -e "  Target: ${CYAN}${TARGET}:${SIP_PORT}${NC}"
echo -e "  Test: ${CYAN}${TEST}${NC}"
echo -e "  Results: ${CYAN}${RUN_DIR}${NC}"
echo -e "${BOLD}──────────────────────────────────────────────────────────────${NC}"

PASS=0
FAIL=0

# ============================================================================
# TEST: Registration
# ============================================================================
test_register() {
    echo -e "\n${BOLD}[TEST] Register Users${NC}"

    local users=("1001:6001" "1002:6002" "1003:6003" "2001:6101" "2002:6102" "2003:6103")

    for entry in "${users[@]}"; do
        local user="${entry%%:*}"
        local port="${entry##*:}"

        echo -n "  Registering $user (port $port)... "

        if sipp "${TARGET}:${SIP_PORT}" \
            -sf "$SCENARIOS_DIR/register_users.xml" \
            -s "$user" \
            -p "$port" \
            -m 1 \
            -nd \
            -timeout 5s \
            > "$RUN_DIR/reg_${user}.log" 2>&1; then
            echo -e "${GREEN}OK${NC}"
            ((PASS++))
        else
            echo -e "${RED}FAIL${NC}"
            ((FAIL++))
        fi
    done
}

# ============================================================================
# TEST: Parking - Retriever hangs up
# ============================================================================
test_park_retriever_bye() {
    echo -e "\n${BOLD}[TEST] Parking: Retriever Hangs Up${NC}"

    local LOCAL_IP
    LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
    if [[ -z "$LOCAL_IP" ]]; then
        LOCAL_IP=$(ifconfig | grep 'inet ' | grep -v '127.0.0.1' | head -1 | awk '{print $2}')
    fi

    echo -e "  Scenario: Parker calls 700, Retriever calls *700, Retriever sends BYE"

    # Start parker first (calls 700, waits for BYE)
    echo -e "  Starting parker (port 7001)..."
    sipp "${TARGET}:${SIP_PORT}" \
        -sf "$SCENARIOS_DIR/uac_park_wait_bye.xml" \
        -i "$LOCAL_IP" \
        -s "parker" \
        -p 7001 \
        -m 1 \
        -nd \
        -timeout 30s \
        -trace_msg \
        -message_file "$RUN_DIR/park_retriever_bye_parker_msgs.log" \
        > "$RUN_DIR/park_retriever_bye_parker.log" 2>&1 &
    local parker_pid=$!

    # Wait for parker to establish call
    sleep 2

    # Start retriever (calls *700, sends BYE after 2s)
    echo -e "  Starting retriever (port 7002)..."
    sipp "${TARGET}:${SIP_PORT}" \
        -sf "$SCENARIOS_DIR/uac_unpark_send_bye.xml" \
        -i "$LOCAL_IP" \
        -inf "$SCRIPT_DIR/data/slot.csv" \
        -s "retriever" \
        -p 7002 \
        -m 1 \
        -nd \
        -timeout 30s \
        -trace_msg \
        -message_file "$RUN_DIR/park_retriever_bye_retriever_msgs.log" \
        > "$RUN_DIR/park_retriever_bye_retriever.log" 2>&1 &
    local retriever_pid=$!

    # Wait for both to complete
    local parker_ok=0
    local retriever_ok=0

    if wait $retriever_pid 2>/dev/null; then
        retriever_ok=1
    fi

    if wait $parker_pid 2>/dev/null; then
        parker_ok=1
    fi

    if [[ $parker_ok -eq 1 && $retriever_ok -eq 1 ]]; then
        echo -e "  ${GREEN}[PASS]${NC} Park/Unpark (retriever BYE)"
        ((PASS++))
    else
        echo -e "  ${RED}[FAIL]${NC} Park/Unpark (retriever BYE)"
        [[ $parker_ok -eq 0 ]] && echo -e "    Parker failed - check $RUN_DIR/park_retriever_bye_parker.log"
        [[ $retriever_ok -eq 0 ]] && echo -e "    Retriever failed - check $RUN_DIR/park_retriever_bye_retriever.log"
        ((FAIL++))
    fi
}

# ============================================================================
# TEST: Parking - Parker hangs up
# ============================================================================
test_park_parker_bye() {
    echo -e "\n${BOLD}[TEST] Parking: Parker Hangs Up${NC}"

    local LOCAL_IP
    LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
    if [[ -z "$LOCAL_IP" ]]; then
        LOCAL_IP=$(ifconfig | grep 'inet ' | grep -v '127.0.0.1' | head -1 | awk '{print $2}')
    fi

    echo -e "  Scenario: Parker calls 700, Retriever calls *700, Parker sends BYE"

    # Start parker (calls 700, waits 4s then sends BYE)
    echo -e "  Starting parker (port 7003)..."
    sipp "${TARGET}:${SIP_PORT}" \
        -sf "$SCENARIOS_DIR/uac_park_send_bye.xml" \
        -i "$LOCAL_IP" \
        -s "parker2" \
        -p 7003 \
        -m 1 \
        -nd \
        -timeout 30s \
        -trace_msg \
        -message_file "$RUN_DIR/park_parker_bye_parker_msgs.log" \
        > "$RUN_DIR/park_parker_bye_parker.log" 2>&1 &
    local parker_pid=$!

    # Wait for parker to establish call
    sleep 2

    # Start retriever (calls *700, waits for BYE)
    echo -e "  Starting retriever (port 7004)..."
    sipp "${TARGET}:${SIP_PORT}" \
        -sf "$SCENARIOS_DIR/uac_unpark_wait_bye.xml" \
        -i "$LOCAL_IP" \
        -inf "$SCRIPT_DIR/data/slot.csv" \
        -s "retriever2" \
        -p 7004 \
        -m 1 \
        -nd \
        -timeout 30s \
        -trace_msg \
        -message_file "$RUN_DIR/park_parker_bye_retriever_msgs.log" \
        > "$RUN_DIR/park_parker_bye_retriever.log" 2>&1 &
    local retriever_pid=$!

    # Wait for both to complete
    local parker_ok=0
    local retriever_ok=0

    if wait $parker_pid 2>/dev/null; then
        parker_ok=1
    fi

    if wait $retriever_pid 2>/dev/null; then
        retriever_ok=1
    fi

    if [[ $parker_ok -eq 1 && $retriever_ok -eq 1 ]]; then
        echo -e "  ${GREEN}[PASS]${NC} Park/Unpark (parker BYE)"
        ((PASS++))
    else
        echo -e "  ${RED}[FAIL]${NC} Park/Unpark (parker BYE)"
        [[ $parker_ok -eq 0 ]] && echo -e "    Parker failed - check $RUN_DIR/park_parker_bye_parker.log"
        [[ $retriever_ok -eq 0 ]] && echo -e "    Retriever failed - check $RUN_DIR/park_parker_bye_retriever.log"
        ((FAIL++))
    fi
}

# ============================================================================
# TEST: Calls (200x calls 100x)
# ============================================================================
test_calls() {
    echo -e "\n${BOLD}[TEST] Call Routing (200x → 100x)${NC}"

    # Call pairs: caller:callee:caller_port:callee_port
    local calls=("2001:1001:6101:6001" "2002:1002:6102:6002" "2003:1003:6103:6003")

    local uas_pids=()
    local uac_pids=()
    local call_info=()

    # Start UAS instances (callees) first - they need to be listening
    echo -e "  Starting UAS (callees)..."

    # Get local IPv4 address for SIPp (avoid IPv6 issues)
    local LOCAL_IP
    LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
    if [[ -z "$LOCAL_IP" ]]; then
        LOCAL_IP=$(ifconfig | grep 'inet ' | grep -v '127.0.0.1' | head -1 | awk '{print $2}')
    fi
    echo -e "    Local IP: $LOCAL_IP"

    for entry in "${calls[@]}"; do
        IFS=':' read -r caller callee caller_port callee_port <<< "$entry"

        echo -e "    UAS for $callee listening on port $callee_port"

        sipp \
            -sf "$SCENARIOS_DIR/uas_answer.xml" \
            -i "$LOCAL_IP" \
            -p "$callee_port" \
            -m 1 \
            -nd \
            -timeout 60s \
            -trace_msg \
            -message_file "$RUN_DIR/uas_${callee}_msgs.log" \
            > "$RUN_DIR/uas_${callee}.log" 2>&1 &
        uas_pids+=($!)
        call_info+=("$caller:$callee")
    done

    # Brief pause for UAS to start listening
    sleep 1

    # Start UAC instances (callers)
    echo -e "  Starting UAC (callers)..."
    for entry in "${calls[@]}"; do
        IFS=':' read -r caller callee caller_port callee_port <<< "$entry"

        echo -e "    $caller → $callee"

        sipp "${TARGET}:${SIP_PORT}" \
            -sf "$SCENARIOS_DIR/uac_call_bye.xml" \
            -i "$LOCAL_IP" \
            -s "$caller" \
            -key callee "$callee" \
            -p "$caller_port" \
            -m 1 \
            -nd \
            -timeout 60s \
            -trace_msg \
            -message_file "$RUN_DIR/uac_${caller}_msgs.log" \
            > "$RUN_DIR/uac_${caller}.log" 2>&1 &
        uac_pids+=($!)
    done

    # Wait for calls to complete
    echo -e "  Waiting for calls (5s duration + setup/teardown)..."

    local uac_results=()
    local uas_results=()

    # Wait for UAC processes first
    for i in "${!uac_pids[@]}"; do
        if wait "${uac_pids[$i]}" 2>/dev/null; then
            uac_results+=("pass")
        else
            uac_results+=("fail")
        fi
    done

    # Give signaling server time to send BYE to UAS (B-leg teardown)
    sleep 1

    # Now wait for UAS processes to complete (they should receive BYE and exit)
    for i in "${!uas_pids[@]}"; do
        if wait "${uas_pids[$i]}" 2>/dev/null; then
            uas_results+=("pass")
        else
            uas_results+=("fail")
        fi
    done

    # Combine results - call passes only if both UAC and UAS succeeded
    local results=()
    for i in "${!uac_results[@]}"; do
        if [[ "${uac_results[$i]}" == "pass" && "${uas_results[$i]}" == "pass" ]]; then
            results+=("pass")
        else
            results+=("fail")
        fi
    done

    # Report results
    echo -e "\n  Results:"
    for i in "${!call_info[@]}"; do
        IFS=':' read -r caller callee <<< "${call_info[$i]}"
        if [[ "${results[$i]}" == "pass" ]]; then
            echo -e "    ${GREEN}[PASS]${NC} $caller → $callee"
            ((PASS++))
        else
            echo -e "    ${RED}[FAIL]${NC} $caller → $callee"
            # Show error hint
            if [[ -f "$RUN_DIR/uac_${caller}.log" ]]; then
                grep -i "error\|fail\|timeout" "$RUN_DIR/uac_${caller}.log" | head -1 | sed 's/^/           /'
            fi
            ((FAIL++))
        fi
    done
}

# ============================================================================
# CLEANUP: Unregister
# ============================================================================
test_unregister() {
    echo -e "\n${BOLD}[CLEANUP] Unregister Users${NC}"

    local users=("1001:6001" "1002:6002" "1003:6003" "2001:6101" "2002:6102" "2003:6103")

    for entry in "${users[@]}"; do
        local user="${entry%%:*}"
        local port="${entry##*:}"

        echo -n "  Unregistering $user... "

        if sipp "${TARGET}:${SIP_PORT}" \
            -sf "$SCENARIOS_DIR/unregister_users.xml" \
            -s "$user" \
            -p "$port" \
            -m 1 \
            -nd \
            -timeout 5s \
            > "$RUN_DIR/unreg_${user}.log" 2>&1; then
            echo -e "${GREEN}OK${NC}"
        else
            echo -e "${YELLOW}SKIP${NC}"
        fi
    done
}

# ============================================================================
# TEST: Ingress gate - unknown source rejected (403)
# ============================================================================
# Assumes default trunk config (this host is NOT a configured trunk peer), so an
# unregistered, non-peer source is rejected with 403. If you have configured this
# host as a trunk peer for the DID tests, this case will see 603 instead.
test_trunk_reject() {
    echo -e "\n${BOLD}[TEST] Ingress Gate: Unknown Source -> 403${NC}"

    local LOCAL_IP
    LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
    if [[ -z "$LOCAL_IP" ]]; then
        LOCAL_IP=$(ifconfig | grep 'inet ' | grep -v '127.0.0.1' | head -1 | awk '{print $2}')
    fi

    echo -n "  Unregistered 'nobody' INVITE (expect 403)... "
    if sipp "${TARGET}:${SIP_PORT}" \
        -sf "$SCENARIOS_DIR/uac_expect_403.xml" \
        -i "$LOCAL_IP" \
        -s "nobody" \
        -key callee "1001" \
        -p 7101 \
        -m 1 \
        -nd \
        -timeout 10s \
        > "$RUN_DIR/trunk_reject.log" 2>&1; then
        echo -e "${GREEN}OK${NC}"
        ((PASS++))
    else
        echo -e "${RED}FAIL${NC} (check $RUN_DIR/trunk_reject.log)"
        ((FAIL++))
    fi
}

# ============================================================================
# TEST: Ingress gate - trunk DID routing (603 unmapped, accept mapped)
# ============================================================================
# Requires this host's IP to be listed as a trunk peer in trunk_peers.json AND
# signaling restarted (config loads at startup). Without that, both sub-tests
# get 403 (unknown source) and fail.
test_trunk_did() {
    echo -e "\n${BOLD}[TEST] Ingress Gate: Trunk DID Routing${NC}"

    local LOCAL_IP
    LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
    if [[ -z "$LOCAL_IP" ]]; then
        LOCAL_IP=$(ifconfig | grep 'inet ' | grep -v '127.0.0.1' | head -1 | awk '{print $2}')
    fi

    echo -e "  ${YELLOW}Prereq:${NC} trunk_peers.json must include host '${LOCAL_IP}' and signaling restarted."

    echo -n "  Trunk INVITE to unmapped DID 9999 (expect 603)... "
    if sipp "${TARGET}:${SIP_PORT}" \
        -sf "$SCENARIOS_DIR/uac_trunk_unmapped.xml" \
        -i "$LOCAL_IP" \
        -s "15550000000" \
        -key callee "9999" \
        -p 7102 \
        -m 1 \
        -nd \
        -timeout 10s \
        > "$RUN_DIR/trunk_unmapped.log" 2>&1; then
        echo -e "${GREEN}OK${NC}"
        ((PASS++))
    else
        echo -e "${RED}FAIL${NC} (check $RUN_DIR/trunk_unmapped.log)"
        ((FAIL++))
    fi

    echo -n "  Trunk INVITE to mapped DID +15551234567 (expect accept)... "
    if sipp "${TARGET}:${SIP_PORT}" \
        -sf "$SCENARIOS_DIR/uac_trunk_mapped.xml" \
        -i "$LOCAL_IP" \
        -s "15550000000" \
        -key callee "+15551234567" \
        -p 7103 \
        -m 1 \
        -nd \
        -timeout 30s \
        -trace_msg \
        -message_file "$RUN_DIR/trunk_mapped_msgs.log" \
        > "$RUN_DIR/trunk_mapped.log" 2>&1; then
        echo -e "${GREEN}OK${NC}"
        ((PASS++))
    else
        echo -e "${RED}FAIL${NC} (check $RUN_DIR/trunk_mapped.log)"
        ((FAIL++))
    fi
}

# ============================================================================
# Main
# ============================================================================
case "$TEST" in
    register)
        test_register
        ;;
    calls)
        test_calls
        ;;
    parking)
        test_park_retriever_bye
        test_park_parker_bye
        ;;
    trunk-reject)
        test_trunk_reject
        ;;
    trunk-did)
        test_trunk_did
        ;;
    trunk)
        test_trunk_reject
        test_trunk_did
        ;;
    all)
        test_register
        test_calls
        test_park_retriever_bye
        test_park_parker_bye
        test_trunk_reject
        ;;
    *)
        echo -e "${RED}Unknown test: $TEST${NC}"
        exit 1
        ;;
esac

# Always cleanup registrations at the end
test_unregister

# Summary
echo -e "${BOLD}──────────────────────────────────────────────────────────────${NC}"
TOTAL=$((PASS + FAIL))
if [[ $FAIL -eq 0 ]]; then
    echo -e "  ${GREEN}TOTAL: ${PASS}/${TOTAL} passed${NC}"
else
    echo -e "  ${RED}TOTAL: ${PASS}/${TOTAL} passed${NC}"
fi
echo -e "  Logs: ${CYAN}${RUN_DIR}${NC}"
echo -e "${BOLD}══════════════════════════════════════════════════════════════${NC}"
echo ""

[[ $FAIL -eq 0 ]]

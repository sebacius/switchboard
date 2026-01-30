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
    echo "Tests: register, calls, all (default)"
    echo ""
    echo "Examples:"
    echo "  $0 192.168.50.181           # Run all"
    echo "  $0 192.168.50.181 register  # Only registration"
    echo "  $0 192.168.50.181 calls     # Only calls"
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
# Main
# ============================================================================
case "$TEST" in
    register)
        test_register
        ;;
    calls)
        test_calls
        ;;
    all)
        test_register
        test_calls
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

import json, os, subprocess, sys, time, hashlib, random, tempfile, signal

S = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(S, "..", ".."))
EXE = ".exe" if os.name == "nt" else ""
SB = os.environ.get("SINGBOX", os.path.join(ROOT, ".sb-bin", "sing-box" + EXE))
XRAY = os.environ.get("XRAY", os.path.join(ROOT, ".xray-bin", "xray" + EXE))
HELPER = os.path.join(S, "helper", "helper" + EXE)
U = "b831381d-6324-4d53-ad4f-8cda48b30811"
PRIV = "cAleecvD332XZnR2GqCz4GHNx7C1m7b1oJ_L-_AF0l4"
PUB = "RbcAOWyICkwKRWgybOnk2ebC9uIBJTh21ZbAAHgrxys"
SID = "0123456789abcdef"
SNI = "www.example.com"
SERVER_PORT, SOCKS_PORT = 18443, 11080


def sb_transport(mode, extra):
    t = {"type": "xhttp", "path": "/xh", "mode": mode}
    t.update(extra or {})
    return t


def sb_server(mode, extra):
    extra = dict(extra or {})
    vl = extra.pop("_vless", {})
    cfg = {
        "log": {"level": "info"},
        "inbounds": [{
            "type": "vless", "listen": "127.0.0.1", "listen_port": SERVER_PORT,
            "users": [{"uuid": U}],
            "tls": {"enabled": True, "server_name": SNI, "reality": {
                "enabled": True, "handshake": {"server": "127.0.0.1", "server_port": 19443},
                "private_key": PRIV, "short_id": [SID]}},
            "transport": sb_transport("auto", extra),
        }],
        "outbounds": [{"type": "direct"}],
    }
    if vl.get("decryption"):
        cfg["inbounds"][0]["decryption"] = vl["decryption"]
    if vl.get("flow"):
        cfg["inbounds"][0]["users"][0]["flow"] = vl["flow"]
    return cfg


def sb_client(mode, extra):
    extra = dict(extra or {})
    vl = extra.pop("_vless", {})
    cfg = {
        "log": {"level": "info"},
        "inbounds": [{"type": "socks", "listen": "127.0.0.1", "listen_port": SOCKS_PORT}],
        "outbounds": [{
            "type": "vless", "server": "127.0.0.1", "server_port": SERVER_PORT, "uuid": U,
            "tls": {"enabled": True, "server_name": SNI,
                    "utls": {"enabled": True, "fingerprint": "chrome"},
                    "reality": {"enabled": True, "public_key": PUB, "short_id": SID, "support_x25519mlkem768": True}},
            "transport": sb_transport(mode, extra),
        }],
    }
    if vl.get("encryption"):
        cfg["outbounds"][0]["encryption"] = vl["encryption"]
    if vl.get("flow"):
        cfg["outbounds"][0]["flow"] = vl["flow"]
    return cfg


def xray_xhttp(mode, extra):
    s = {"path": "/xh", "mode": mode}
    s.update(extra or {})
    return s


def xray_server(mode, extra):
    extra = dict(extra or {})
    vl = extra.pop("_vless", {})
    cfg = {
        "log": {"loglevel": "info"},
        "inbounds": [{
            "listen": "127.0.0.1", "port": SERVER_PORT, "protocol": "vless",
            "settings": {"clients": [{"id": U}], "decryption": "none"},
            "streamSettings": {"network": "xhttp", "security": "reality",
                               "realitySettings": {"target": "127.0.0.1:19443", "serverNames": [SNI],
                                                   "privateKey": PRIV, "shortIds": [SID]},
                               "xhttpSettings": xray_xhttp("auto", extra)},
        }],
        "outbounds": [{"protocol": "freedom", "settings": {"finalRules": [{"action": "allow"}]}}],
    }
    if vl.get("decryption"):
        cfg["inbounds"][0]["settings"]["decryption"] = vl["decryption"]
    if vl.get("flow"):
        cfg["inbounds"][0]["settings"]["clients"][0]["flow"] = vl["flow"]
    return cfg


def xray_client(mode, extra):
    extra = dict(extra or {})
    vl = extra.pop("_vless", {})
    cfg = {
        "log": {"loglevel": "info"},
        "inbounds": [{"listen": "127.0.0.1", "port": SOCKS_PORT, "protocol": "socks", "settings": {"udp": False}}],
        "outbounds": [{
            "protocol": "vless",
            "settings": {"vnext": [{"address": "127.0.0.1", "port": SERVER_PORT, "users": [{"id": U, "encryption": "none"}]}]},
            "streamSettings": {"network": "xhttp", "security": "reality",
                               "realitySettings": {"serverName": SNI, "fingerprint": "chrome", "publicKey": PUB, "shortId": SID},
                               "xhttpSettings": xray_xhttp(mode, extra)},
        }],
    }
    user = cfg["outbounds"][0]["settings"]["vnext"][0]["users"][0]
    if vl.get("encryption"):
        user["encryption"] = vl["encryption"]
    if vl.get("flow"):
        user["flow"] = vl["flow"]
    return cfg


def start(kind, role, cfg, workdir, logs):
    path = os.path.join(workdir, f"{role}.json")
    with open(path, "w") as f:
        json.dump(cfg, f, indent=1)
    log = open(os.path.join(workdir, f"{role}.log"), "w")
    logs.append(log)
    if kind == "sb":
        cmd = [SB, "run", "-c", path]
    else:
        cmd = [XRAY, "run", "-c", path]
    return subprocess.Popen(cmd, stdout=log, stderr=subprocess.STDOUT)


def clean_env():
    # NO_PROXY=127.0.0.1 in the environment would make curl bypass the SOCKS proxy for loopback targets
    env = dict(os.environ)
    for key in list(env):
        if key.lower() in ("no_proxy", "http_proxy", "https_proxy", "all_proxy"):
            del env[key]
    return env


def curl(*args, timeout=120):
    r = subprocess.run(["curl", "-sS", "--max-time", str(timeout), "--socks5-hostname", f"127.0.0.1:{SOCKS_PORT}", *args],
                       capture_output=True, timeout=timeout + 10, env=clean_env())
    return r.returncode, r.stdout, r.stderr.decode(errors="replace")


def udp_echo():
    import socket, struct
    ctl = socket.create_connection(("127.0.0.1", SOCKS_PORT), timeout=10)
    ctl.sendall(b"\x05\x01\x00")
    assert ctl.recv(2) == b"\x05\x00"
    ctl.sendall(b"\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00")
    rep = ctl.recv(10)
    assert rep[1] == 0, rep
    port = struct.unpack(">H", rep[8:10])[0]
    u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    u.settimeout(10)
    payload = os.urandom(900)
    hdr = b"\x00\x00\x00\x01" + socket.inet_aton("127.0.0.1") + struct.pack(">H", 19090)
    ok = 0
    for _ in range(5):
        u.sendto(hdr + payload, ("127.0.0.1", port))
        try:
            data, _ = u.recvfrom(65536)
            ok += data[10:] == payload
        except (socket.timeout, ConnectionResetError):
            pass
    ctl.close()
    return ok


def expected_blob(n):
    # must match helper: math/rand seeded with n, 32KiB reads; here we just trust server-side sha print
    return None


def run_case(server_kind, client_kind, mode, s_extra=None, c_extra=None, size=24 * 1024 * 1024):
    workdir = tempfile.mkdtemp(prefix="xhttp_")
    logs, procs = [], []
    ok = False
    detail = ""
    try:
        helper = subprocess.Popen([HELPER], stdout=open(os.path.join(workdir, "helper.log"), "w"), stderr=subprocess.STDOUT)
        procs.append(helper)
        pick = lambda e, k: (e or {}).get(k) if isinstance(e, dict) and ("sb" in e or "xray" in e) else e
        scfg = (sb_server if server_kind == "sb" else xray_server)(mode, pick(c_extra, server_kind))
        ccfg = (sb_client if client_kind == "sb" else xray_client)(mode, pick(c_extra, client_kind))
        procs.append(start(server_kind, "server", scfg, workdir, logs))
        time.sleep(1.5)
        procs.append(start(client_kind, "client", ccfg, workdir, logs))
        time.sleep(1.5)
        # 1) reality sanity: direct probe of the server port as a browser would (not through proxy)
        # 2) download
        t0 = time.time()
        rc, out, err = curl(f"http://127.0.0.1:19080/blob?n={size}")
        dt = time.time() - t0
        if rc != 0 or len(out) != size:
            detail += f"download failed rc={rc} len={len(out)} err={err.strip()}"
            return False, detail, workdir
        want = open(os.path.join(workdir, "helper.log")).read()
        sha = hashlib.sha256(out).hexdigest()
        if sha not in want:
            detail += f"download sha mismatch ({sha}) helper log: {want[-300:]}"
            return False, detail, workdir
        mbps_down = size * 8 / dt / 1e6
        # 3) upload
        data = os.urandom(size // 2)
        up = os.path.join(workdir, "up.bin")
        open(up, "wb").write(data)
        t0 = time.time()
        rc, out, err = curl("-X", "POST", "--data-binary", f"@{up}", "http://127.0.0.1:19080/up")
        dt = time.time() - t0
        if rc != 0:
            detail = f"upload failed rc={rc} err={err.strip()}"
            return False, detail, workdir
        got = out.decode().split()
        if got != [str(len(data)), hashlib.sha256(data).hexdigest()]:
            detail = f"upload mismatch: {out!r}"
            return False, detail, workdir
        mbps_up = len(data) * 8 / dt / 1e6
        if (c_extra or {}).get("_vless", {}).get("flow") or isinstance(c_extra, dict) and (c_extra.get("sb") or {}).get("_vless", {}).get("flow"):
            rc, out, err = curl("-k", f"https://127.0.0.1:19081/blob?n={size}")
            if rc != 0 or len(out) != size or hashlib.sha256(out).hexdigest() not in open(os.path.join(workdir, "helper.log")).read():
                return False, f"tls download (vision direct) failed rc={rc} len={len(out)} err={err.strip()}", workdir
            data = os.urandom(size // 2)
            open(up, "wb").write(data)
            rc, out, err = curl("-k", "-X", "POST", "--data-binary", f"@{up}", "https://127.0.0.1:19081/up")
            if rc != 0 or out.decode().split() != [str(len(data)), hashlib.sha256(data).hexdigest()]:
                return False, f"tls upload (vision direct) failed rc={rc} out={out!r} err={err.strip()}", workdir
            detail += "tls ok; "
        if (c_extra or {}).get("_vless") is not None or isinstance(c_extra, dict) and "sb" in c_extra and client_kind == "sb":
            got = udp_echo()
            if got < 4:
                return False, f"udp echo failed ({got}/5)", workdir
            detail += f"udp {got}/5; "
        # 4) a few small sequential requests (connection setup path)
        t0 = time.time()
        for _ in range(8):
            rc, out, err = curl("http://127.0.0.1:19080/blob?n=1000")
            if rc != 0 or len(out) != 1000:
                detail = f"small request failed rc={rc} err={err.strip()}"
                return False, detail, workdir
        lat = (time.time() - t0) / 8
        detail += f"down {mbps_down:.0f} Mbit/s, up {mbps_up:.0f} Mbit/s, small req {lat*1000:.0f} ms"
        return True, detail, workdir
    finally:
        for p in reversed(procs):
            p.kill()
        for p in procs:
            try:
                p.wait(timeout=5)
            except Exception:
                pass
        for l in logs:
            l.close()


if __name__ == "__main__":
    cases = []
    for mode in ["auto", "packet-up", "stream-up", "stream-one"]:
        cases.append(("sb", "sb", mode, None, None))
        cases.append(("xray", "sb", mode, None, None))
        cases.append(("sb", "xray", mode, None, None))
    variants = {
        "obfs-cookie-tokenish": {"sb": {"x_padding_obfs_mode": True, "x_padding_placement": "cookie", "x_padding_key": "pk", "x_padding_method": "tokenish"},
                                 "xray": {"xPaddingObfsMode": True, "xPaddingPlacement": "cookie", "xPaddingKey": "pk", "xPaddingMethod": "tokenish"}},
        "obfs-query": {"sb": {"x_padding_obfs_mode": True, "x_padding_placement": "query", "x_padding_key": "pk"},
                       "xray": {"xPaddingObfsMode": True, "xPaddingPlacement": "query", "xPaddingKey": "pk"}},
        "obfs-header": {"sb": {"x_padding_obfs_mode": True, "x_padding_placement": "header", "x_padding_header": "X-Foo"},
                        "xray": {"xPaddingObfsMode": True, "xPaddingPlacement": "header", "xPaddingHeader": "X-Foo"}},
        "session-header-seq-cookie": {"sb": {"session_id_placement": "header", "seq_placement": "cookie"},
                                      "xray": {"sessionIDPlacement": "header", "seqPlacement": "cookie"}},
        "session-query-seq-query-table": {"sb": {"session_id_placement": "query", "seq_placement": "query", "session_id_table": "hex", "session_id_length": "16-20"},
                                          "xray": {"sessionIDPlacement": "query", "seqPlacement": "query", "sessionIDTable": "hex", "sessionIDLength": "16-20"}},
        "small-posts": {"sb": {"sc_max_each_post_bytes": 20000, "sc_min_posts_interval_ms": 1},
                        "xray": {"scMaxEachPostBytes": 20000, "scMinPostsIntervalMs": 1}},
        "xmux": {"sb": {"xmux": {"max_connections": 2, "h_max_request_times": 50}},
                 "xray": {"xmux": {"maxConnections": 2, "hMaxRequestTimes": 50}}},
        "ua-firefox-headers": {"sb": {"headers": {"User-Agent": "firefox", "X-Test": "1"}},
                               "xray": {"headers": {"User-Agent": "firefox", "X-Test": "1"}}},
    }
    dl_tls = {"enabled": True, "server_name": SNI, "utls": {"enabled": True, "fingerprint": "chrome"},
              "reality": {"enabled": True, "public_key": PUB, "short_id": SID, "support_x25519mlkem768": True}}
    variants["download-settings"] = {
        "sb": {"download": {"server": "127.0.0.1", "server_port": SERVER_PORT, "tls": dl_tls, "path": "/xh"}},
        "xray": {"downloadSettings": {"address": "127.0.0.1", "port": SERVER_PORT, "network": "xhttp", "security": "reality",
                                      "realitySettings": {"serverName": SNI, "fingerprint": "chrome", "publicKey": PUB, "shortId": SID},
                                      "xhttpSettings": {"path": "/xh"}}},
    }
    for vname, extra in variants.items():
        for mode in ["packet-up", "stream-up"]:
            cases.append(("xray", "sb", mode, None, extra, vname))
            cases.append(("sb", "xray", mode, None, extra, vname))
    ENC = "mlkem768x25519plus.native.0rtt.w4iFhOnPVWx7VwVMRkx2lKF45-w8ISlIkMRTJ3zHDkak4tsOFKOPqnu07GUlJrcemONDOpjDisA-bpk0IdRpXfrJMpMoQlCX4WROQMXM0ByCV8K_ttGeYBIUt8mikkpg8JZ5PPd4KXVnl9gAculFExdgm5BwUrNdrSyw6ZmnwHy0bpC_p1fHN0hy_RaIPcFcxfu5eXN2sBeKqdUR50h-V3F1TBvO7FFsBmC6m5wdC7Y72IG3_1LEAfnMBMeD0iA0zUvBtoJ0pFswyawzBZM0W9gOi6te3NyyKRpJn7WDFto28sRdnLMpRdnEw1o3PEOPibxF8SNhjSSxZIe8l2UpNvpY_OhjsvwOkuCPjXNerFNU6wVMt_yGukJ-XXu4O5lN_zopliVL5zDMlJirgfVOqqAWBxq19EXAqXEfiKtoUAVHK6ETh1a3ijZv6PyCbWsHNwKDeanJRCpeiEVjhdcJHSaM41HEe1F578yk9TShwIgNiKiiMuG6KSlRuAl_E2OKYTa3p4qokOUOjzJdzJVixYhyMAuspXaxhBFRblNOafSkP7wZQQkBxkLPGQpjvaRPi1mD3vSkz1RvBDPFDre3mbUZs1QN-yulp9UMesoK2XWUYpwHzyTLaNCk1WJNPspXhKVZ9XofE9cnK6YuIXEAbKeBGGMkQPBFf3SC7sKAt9sGAByeM-AO4YxO5SBgWkpfLKx_dFdPHxw04lt6XzjKwggxz7GYeSxQ-kB6vzSWIJhlceKm2qe8T2K_DrwGPeK2XQEHjZxK39azBEHJMPe-cQtJJNHJQkwZPvYQ8MO98qRULQlz8BJ5Mqs5XoM_qcM9-mxrQrhRKBS4VuUpGIw3PzK2qgOcFmdI8kA3mXhwXDKY7QakNqjIioVjA8ACRvuSNeNplehWPPu-CpcLQox5OeZMHMeDWdrJIgpO-oSsIfC2fuy3GMK6utMassed0lEwBFBoVbhyL-cOsXF7-mBGJ_dopTapSHG9RIJPE8GhzcE59RdIo7K4SpMtUBsPSmel0hZcF-MZPyt7HeczDNRr84aH_qpGgiFaPESWD2Nu4AhFP4ihXwuihpdicjdrbPpKlpNGKYeIohG24WaFyie2PhaJPfAZoDlNfad1MKlozeFFSjxFskQvaKOm3Gqu3UPKCRUq5uIjfoVfATEwATou7aV0dhqGflg9zPehQkHHjpdyN7KXKEB1hklpF9Rf-so_Tcpq_5gcl5OSnhqElTlDBSLG8rQPuGAMqmymXloD4YtwvitaynEI-vyHJ-tq66VZlzOV34pI8DFm4CixjMxxslxKuRM5ADELPYdIYloKtYG0I1BO5QYcdOk-s7Jpn2l8m6Rdeoa_4kCquUcJl_ixG_O-4rJGFaMTkKEVPeV9omJvpRK528fAxhyijGFtpqYVRibHbdRwf5y-wqmm9zQ0hxO40FOtI4Wb0DqCWeeP5IBJRaJNRVZ9zGsmuuqoeYmwnyNx0dcTMgImdadJOJYqOVlzDishIgJeyUZMDkaG84EgzbqUCCuYnribmcBgUHcSVycIMT4f0BEWvRut_ARSrJB2c1iHG0N273xjLGOXqjs"
    ENC_X = "mlkem768x25519plus.native.0rtt.FLYxhu3p199hbFhv7NE-1WCYAvvDDg8uj5tRaMykE1o"
    DEC = "mlkem768x25519plus.native.600s.J1ywlafDAS28zDZ6A43GnXk9iP3djxgSPIMu7YZX16neI11S3anWnCbaSlEoLqvWVr4CfOR2N0Waq3dudRcZYw"
    DEC_X = "mlkem768x25519plus.native.600s.8CreTmnN6lIHE-hrZ6weE_hE_-pKyukIh_lPjsCRNns"
    for ename, enc, dec in [("enc-mlkem", ENC, DEC), ("enc-x25519", ENC_X, DEC_X)]:
        for flow in ["", "xtls-rprx-vision"]:
            for mode in ["auto", "packet-up", "stream-up"]:
                cases.append(("xray", "sb", mode, None, {"sb": {"_vless": {"encryption": enc, "flow": flow}}, "xray": {"_vless": {"decryption": dec, "flow": flow}}}, ename + ("-vision" if flow else "")))
                # sing-box server (decryption) with sing-box and Xray clients
                cases.append(("sb", "sb", mode, None, {"sb": {"_vless": {"encryption": enc, "decryption": dec, "flow": flow}}}, "sbserver-" + ename + ("-vision" if flow else "")))
                cases.append(("sb", "xray", mode, None, {"sb": {"_vless": {"decryption": dec, "flow": flow}}, "xray": {"_vless": {"encryption": enc, "flow": flow}}}, "sbserver-" + ename + ("-vision" if flow else "")))
    for xm in ["xorpub", "random"]:
        e, d = ENC_X.replace(".native.", f".{xm}."), DEC_X.replace(".native.", f".{xm}.")
        for flow in ["", "xtls-rprx-vision"]:
            tag = f"enc-{xm}" + ("-vision" if flow else "")
            cases.append(("xray", "sb", "packet-up", None, {"sb": {"_vless": {"encryption": e, "flow": flow}}, "xray": {"_vless": {"decryption": d, "flow": flow}}}, tag))
            cases.append(("sb", "xray", "stream-up", None, {"sb": {"_vless": {"decryption": d, "flow": flow}}, "xray": {"_vless": {"encryption": e, "flow": flow}}}, "sbserver-" + tag))
    only = sys.argv[1:]
    failed = 0
    for case in cases:
        sk, ck, mode, se, ce = case[:5]
        name = f"server={sk:4} client={ck:4} mode={mode} {case[5] if len(case) > 5 else ''}".rstrip()
        if only and not any(o in name for o in only):
            continue
        ok, detail, wd = run_case(sk, ck, mode, se, ce)
        print(("PASS " if ok else "FAIL ") + name + " :: " + detail + ("" if ok else f"  [{wd}]"), flush=True)
        failed += not ok
    sys.exit(1 if failed else 0)

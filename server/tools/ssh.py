# Minimal SSH runner for the app server (paramiko; password from env MF_SSH_PASS).
# Usage: python ssh.py run "command"          — exec and print output
#        python ssh.py put local remote      — sftp upload
import os, sys, warnings
warnings.filterwarnings("ignore")
import paramiko

HOST, PORT, USER = "43.155.234.117", 22, "ubuntu"
PASS = os.environ.get("MF_SSH_PASS")

def client():
    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    c.connect(HOST, PORT, USER, PASS, timeout=25, banner_timeout=25, auth_timeout=25)
    return c

def main():
    mode = sys.argv[1]
    c = client()
    if mode == "run":
        cmd = sys.argv[2]
        stdin, stdout, stderr = c.exec_command(cmd, timeout=1200, get_pty=False)
        out = stdout.read().decode("utf-8", "replace")
        err = stderr.read().decode("utf-8", "replace")
        code = stdout.channel.recv_exit_status()
        sys.stdout.write(out)
        if err.strip():
            sys.stdout.write("\n[stderr] " + err[-2000:])
        sys.stdout.write(f"\n[exit {code}]\n")
    elif mode == "put":
        sftp = c.open_sftp()
        sftp.put(sys.argv[2], sys.argv[3])
        print("uploaded", sys.argv[2], "->", sys.argv[3])
        sftp.close()
    c.close()

if __name__ == "__main__":
    main()

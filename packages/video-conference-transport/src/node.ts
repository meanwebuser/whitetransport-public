import { spawn } from 'node:child_process';
import type { ChildProcess } from 'node:child_process';
import type {
  RuntimeCommand,
  RuntimeExit,
  RuntimeLauncher,
  RuntimeProcess,
} from './index.js';

export interface NodeRuntimeLauncherOptions {
  readonly defaultCwd?: string;
  readonly defaultEnv?: Readonly<Record<string, string>>;
  readonly stdio?: 'ignore' | 'pipe' | 'inherit';
}

export class NodeRuntimeLauncher implements RuntimeLauncher {
  private readonly defaultCwd?: string;
  private readonly defaultEnv: Readonly<Record<string, string>>;
  private readonly stdio: 'ignore' | 'pipe' | 'inherit';

  constructor(options: NodeRuntimeLauncherOptions = {}) {
    this.defaultCwd = options.defaultCwd;
    this.defaultEnv = options.defaultEnv ?? {};
    this.stdio = options.stdio ?? 'ignore';
  }

  /**
   * Launches a runtime command as a supervised Node child process.
   *
   * @param command Command descriptor from the video-conference runtime adapter.
   * @returns Runtime process handle with stop and exit tracking.
   */
  async launch(command: RuntimeCommand): Promise<RuntimeProcess> {
    const child = spawn(command.executable, [...command.args], {
      cwd: command.cwd ?? this.defaultCwd,
      env: {
        ...process.env,
        ...this.defaultEnv,
        ...command.env,
      },
      stdio: this.stdio,
    });

    return new NodeRuntimeProcess(command, child);
  }
}

class NodeRuntimeProcess implements RuntimeProcess {
  readonly pid?: number;
  readonly command: RuntimeCommand;
  readonly exited: Promise<RuntimeExit>;
  private readonly child: ChildProcess;

  constructor(command: RuntimeCommand, child: ChildProcess) {
    this.command = command;
    this.child = child;
    this.pid = child.pid;
    this.exited = new Promise<RuntimeExit>((resolve) => {
      child.once('exit', (code, signal) => {
        resolve({ code, signal: signal ?? undefined });
      });
      child.once('error', (error) => {
        resolve({ code: null, reason: error.message });
      });
    });
  }

  async stop(signal = 'SIGTERM'): Promise<void> {
    if (this.child.exitCode !== null || this.child.signalCode !== null) return;
    this.child.kill(signal as NodeJS.Signals);
    await this.exited;
  }
}

import Darwin
import Foundation

private func run() -> Int32 {
    guard CommandLine.arguments.count == 4 else { return 64 }
    let lockPath = CommandLine.arguments[1]
    let readyMarkerPath = CommandLine.arguments[2]
    let acquiredMarkerPath = CommandLine.arguments[3]
    let descriptor = lockPath.withCString { Darwin.open($0, O_CREAT | O_RDWR, S_IRUSR | S_IWUSR) }
    guard descriptor >= 0 else { return 65 }
    defer { Darwin.close(descriptor) }
    guard fcntl(descriptor, F_SETFD, FD_CLOEXEC) == 0 else { return 66 }
    do {
        try Data("ready".utf8).write(to: URL(fileURLWithPath: readyMarkerPath), options: .atomic)
    } catch {
        return 67
    }
    guard flock(descriptor, LOCK_EX) == 0 else { return 68 }
    defer { flock(descriptor, LOCK_UN) }
    do {
        try Data("acquired".utf8).write(to: URL(fileURLWithPath: acquiredMarkerPath), options: .atomic)
    } catch {
        return 69
    }
    return 0
}

exit(run())

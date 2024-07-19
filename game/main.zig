const rl = @import("raylib");
const std = @import("std");
const print = std.debug.print;
const Child = std.process.Child;
const ArrayList = std.ArrayList;

pub fn main() anyerror!void {
    const screenWidth = 1280;
    const screenHeight = 720;

    rl.initWindow(screenWidth, screenHeight, "hh-scope");
    defer rl.closeWindow();

    var gpa = std.heap.GeneralPurposeAllocator(.{}){};
    const allocator = gpa.allocator();
    defer {
        const check = gpa.deinit();
        switch (check) {
            .leak => {
                std.debug.print("Allocator deinit error\n", .{});
            },
            .ok => {},
        }
    }
    var state = State{};
    try start_server(allocator, &state);

    rl.setTargetFPS(60);
    while (!rl.windowShouldClose()) { // Detect window close button or ESC key
        rl.beginDrawing();
        defer rl.endDrawing();

        rl.clearBackground(rl.Color.white);
        rl.drawFPS(0, 0);
    }
}

const XY = struct { x: i32, y: i32 };

const Plane = struct {
    time: u32, // last time position was materialized
    p: XY,
    wantHeading: u16,
    heading: u16,
};

const State = struct { now: u32 = 0, planes: []Plane = &[_]Plane{}, mu: std.Thread.Mutex = .{} };

fn start_server(allocator: std.mem.Allocator, state: *State) !void {
    const thread = try std.Thread.spawn(.{}, read_data, .{ allocator, state });
    _ = thread;
}

fn read_data(allocator: std.mem.Allocator, state: *State) !void {
    const argv = [_][]const u8{"./server"};

    var child = Child.init(&argv, allocator);
    child.stdout_behavior = .Pipe;
    child.stderr_behavior = .Inherit;

    try child.spawn();
    if (child.stdout == null) {
        @panic("no stdout found");
    }

    var header: [8]u8 = [_]u8{0} ** 8;
    while (child.term == null) {
        const n = try child.stdout.?.read(&header);
        print("read {} bytes", .{n});
        state.mu.lock();
        defer state.mu.unlock();
        // const now = r_u32(header[0..4]);
        const plane_count = r_u32(header[4..8]);
        const planes = try allocator.alloc(u8, 16 * plane_count);
        _ = try child.stdout.?.read(planes);
    }
}

fn r_u16(b: []u8) u16 {
    return b[0] | b[1] << 8;
}

fn r_u32(b: []u8) u32 {
    return @as(u32, b[0]) | (@as(u32, b[1]) << 8) | (@as(u32, b[2]) << 16) | (@as(u32, b[3]) << 24);
}

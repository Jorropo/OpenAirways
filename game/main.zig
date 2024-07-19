const rl = @import("raylib");
const std = @import("std");
const print = std.debug.print;

const Thread = std.Thread;
const Allocator = std.mem.Allocator;
const Child = std.process.Child;
const ArrayList = std.ArrayList;

const screen_w = 1280;
const screen_h = 720;

pub fn main() anyerror!void {
    rl.initWindow(screen_w, screen_h, "hh-scope");
    defer rl.closeWindow();

    var gpa = std.heap.GeneralPurposeAllocator(.{}){};
    const allocator = gpa.allocator();
    defer {
        const check = gpa.deinit();
        switch (check) {
            .leak => std.debug.print("Allocator deinit error\n", .{}),
            .ok => {},
        }
    }

    const server_args = [_][]const u8{ "./game-server", "-debug-tickmode", "realtime" };
    var proc = Child.init(&server_args, allocator);
    proc.stdout_behavior = .Pipe;
    proc.stderr_behavior = .Inherit;
    var state = State{};

    var thread = try start_server(allocator, &proc, &state);

    rl.setTargetFPS(60);

    const plane_img = rl.loadTexture("assets/plane_1.png");
    const plane_w: f32 = @floatFromInt(plane_img.width);
    const plane_h: f32 = @floatFromInt(plane_img.height);
    const plane_size = rl.Rectangle{ .x = 0, .y = 0, .width = plane_w, .height = plane_h };

    while (!rl.windowShouldClose()) { // Detect window close button or ESC key
        rl.beginDrawing();

        defer rl.endDrawing();
        defer rl.drawFPS(8, 8); // always draw last

        rl.clearBackground(rl.Color.white);

        state.mu.lock();
        for (state.planes) |plane| {
            try plane.draw(allocator, plane_img, plane_size, true);
        }
        state.mu.unlock();
    }

    _ = try proc.kill();
    thread.join();
}

const XY = struct { x: f32 = 0, y: f32 = 0 };

const canvas_w: f32 = 960;
const canvas_h: f32 = 560;

const Plane = struct {
    id: u32 = 0,
    pos: XY = .{},
    want_heading: u16 = 0,
    heading: u16 = 0,

    fn draw(self: Plane, allocator: Allocator, img: rl.Texture, src: rl.Rectangle, draw_debug: bool) !void {
        const origin = rl.Vector2{ .x = src.width / 2, .y = src.height / 2 };

        // convert XY to coords between 0 and screen size
        // and flip so positive y is upwards
        const target = rl.Rectangle{
            .x = (self.pos.x + canvas_w / 2) * screen_w / canvas_w,
            .y = (-self.pos.y + canvas_h / 2) * screen_h / canvas_h,
            .width = src.width,
            .height = src.height,
        };

        const rot: f32 = @as(f32, @floatFromInt(self.heading)) / 65536;
        rl.drawTexturePro(img, src, target, origin, rot * 360, rl.Color.white);

        if (draw_debug) {
            const pos = try std.fmt.allocPrintZ(allocator, "id={}, pos=[{d}, {d}]", .{ self.id, self.pos.x, self.pos.y });
            rl.drawText(pos, @intFromFloat(target.x + target.width / 2), @intFromFloat(target.y - target.height / 2), 16, rl.Color.red);
            allocator.free(pos);
        }
    }
};

const State = struct { now: u32 = 0, planes: []Plane = &[_]Plane{}, mu: Thread.Mutex = .{} };

fn start_server(allocator: Allocator, proc: *Child, state: *State) !Thread {
    const thread = try Thread.spawn(.{}, read_data, .{ allocator, proc, state });
    return thread;
}

fn read_data(allocator: Allocator, proc: *Child, state: *State) !void {
    try proc.spawn();

    var header: [8]u8 = [_]u8{0} ** 8;
    var buffered_state = State{};

    while (proc.term == null) {
        const out = proc.stdout orelse break;
        _ = out.read(&header) catch break;

        const plane_count = r_u32(header[4..8]);
        const raw_planes = try allocator.alloc(u8, 16 * plane_count);
        defer allocator.free(raw_planes);

        _ = out.read(raw_planes) catch break;

        const now = r_u32(header[0..4]);
        buffered_state.now = now;

        const old = buffered_state.planes;
        buffered_state.planes = try allocator.alloc(Plane, plane_count);

        const plane_size = 4 + // id
            4 + // x
            4 + // y
            2 + // wantHeading
            2; // heading

        for (0..plane_count) |i| {
            const offset = plane_size * i;

            const id = r_u32(raw_planes[offset .. offset + 4]);
            const x = r_f32(raw_planes[offset + 4 .. offset + 8]);
            const y = r_f32(raw_planes[offset + 8 .. offset + 12]);

            const want_heading = r_u16(raw_planes[offset + 12 .. offset + 14]);
            const heading = r_u16(raw_planes[offset + 14 .. offset + 16]);

            buffered_state.planes[i].id = id;
            buffered_state.planes[i].pos.x = x;
            buffered_state.planes[i].pos.y = y;
            buffered_state.planes[i].want_heading = want_heading;
            buffered_state.planes[i].heading = heading;
        }

        state.mu.lock();
        state.*.now = buffered_state.now;
        state.*.planes = buffered_state.planes;
        allocator.free(old);
        state.mu.unlock();
    }

    allocator.free(buffered_state.planes);
}

fn r_u16(b: []u8) u16 {
    return @as(u16, b[0]) | @as(u16, b[1]) << 8;
}

fn r_u32(b: []u8) u32 {
    return @as(u32, b[0]) | (@as(u32, b[1]) << 8) | (@as(u32, b[2]) << 16) | (@as(u32, b[3]) << 24);
}

fn r_f32(b: []u8) f32 {
    return @floatFromInt(r_i32(b));
}

fn r_i32(b: []u8) i32 {
    return @as(i32, b[0]) | (@as(i32, b[1]) << 8) | (@as(i32, b[2]) << 16) | (@as(i32, b[3]) << 24);
}

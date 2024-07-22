const rl = @import("raylib");
const zm = @import("zmath");

const std = @import("std");
const print = std.debug.print;
const math = std.math;

const Thread = std.Thread;
const Allocator = std.mem.Allocator;
const Child = std.process.Child;
const ArrayList = std.ArrayList;

const V2 = rl.Vector2;

const screen = V2.init(1280, 720);
const canvas = V2.init(960, 560);

pub fn main() anyerror!void {
    rl.setConfigFlags(rl.ConfigFlags{
        .msaa_4x_hint = true,
        .vsync_hint = true,
    });
    rl.initWindow(screen.x, screen.y, "OpenAirways");
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

    var server_args = try std.process.argsAlloc(allocator);
    const programName = server_args[0];
    const const_server_args: [][:0]const u8 = server_args;
    const_server_args[0] = "./game-server";

    var proc = Child.init(const_server_args, allocator);
    proc.stdin_behavior = .Pipe;
    proc.stdout_behavior = .Pipe;
    proc.stderr_behavior = .Inherit;
    var state = State{};

    var thread = try start_server(allocator, &proc, &state);

    const plane_img = rl.loadTexture("assets/plane_1.png");
    const plane_w: f32 = @floatFromInt(plane_img.width);
    const plane_h: f32 = @floatFromInt(plane_img.height);
    const plane_size = V2.init(plane_w, plane_h);

    var input_state: InputState = .none;

    state.initFinished.wait();
    server_args[0] = programName; // restore for argsFree
    std.process.argsFree(allocator, server_args);
    const in = proc.stdin orelse return;

    while (!rl.windowShouldClose()) { // Detect window close button or ESC key
        rl.beginDrawing();
        const frameClock: i64 = @intCast(state.timer.read());

        const mouse_pos = rl.getMousePosition();
        switch (input_state) {
            .plane_target => |s| blk: {
                if (rl.isMouseButtonReleased(rl.MouseButton.mouse_button_left)) {
                    input_state = .none;
                    try RPC.give_plane_heading(
                        in,
                        s.plane_id,
                        s.start,
                        mouse_pos,
                    );
                    break :blk;
                }
                input_state.plane_target.current = mouse_pos;
            },

            .none => {
                if (rl.isMouseButtonPressed(rl.MouseButton.mouse_button_left)) {
                    const clicked = Plane.intersecting_plane(&state, plane_size, mouse_pos);
                    if (clicked) |p| {
                        input_state = .{ .plane_target = .{
                            .plane_id = p.id,
                            .start = mouse_pos,
                            .current = mouse_pos,
                        } };
                    }
                }
            },
        }

        defer rl.endDrawing();
        defer rl.drawFPS(8, 8); // always draw last

        rl.clearBackground(rl.Color.white);

        state.mu.lock();
        const nanosPerTick = std.time.ns_per_s / state.tickRate;
        const deltans = frameClock - @as(i64, state.now - state.timerBase) * nanosPerTick;
        state.deltaTicks = @as(f32, @floatFromInt(deltans)) / @as(f32, @floatFromInt(nanosPerTick));

        var highlighted_plane: Plane = undefined;
        for (state.planes) |plane| {
            var highlight = false;
            if (input_state == .plane_target and input_state.plane_target.plane_id == plane.id) {
                highlight = true;
                highlighted_plane = plane;
            }
            try plane.draw(allocator, &state, plane_img, plane_size, highlight, true);
        }

        if (input_state == .plane_target) {
            const loc = highlighted_plane.canvas_loc(&state);
            rl.drawLineEx(loc, input_state.plane_target.current, 4, rl.Color.red);
        }

        state.mu.unlock();
    }

    _ = try proc.kill();
    thread.join();
}

const InputPlaneTarget = struct {
    plane_id: u32,
    start: V2,
    current: V2,
};

const InputState = union(enum) {
    plane_target: InputPlaneTarget,
    none,
};

const Plane = struct {
    id: u32 = 0,
    pos: V2 = .{},
    want_heading: u16 = 0,
    heading: u16 = 0,

    fn draw(self: Plane, allocator: Allocator, state: *State, img: rl.Texture, size: V2, highlight: bool, draw_debug: bool) !void {
        const full_image = rect(V2.zero(), size);
        const origin = size.scale(0.5);
        const center = self.canvas_loc(state);

        img.drawPro(
            full_image,
            rect(center, size),
            origin,
            self.rot(),
            rl.Color.white,
        );

        const top_left = center.subtract(origin);
        const top_right = top_left.add(V2.init(size.x, 0));

        if (highlight) {
            rl.drawRectangleRoundedLinesEx(rect(top_left, size), 64, 64, 4, rl.Color.red);
        }

        if (draw_debug) {
            const debug_text = try std.fmt.allocPrintZ(allocator, "id={}, pos=[{d}, {d}]", .{ self.id, self.pos.x, self.pos.y });
            rl.drawTextEx(rl.getFontDefault(), debug_text, top_right, 16, 1, rl.Color.red);
            allocator.free(debug_text);
        }
    }

    fn intersecting_plane(state: *State, size: V2, mouse_pos: V2) ?Plane {
        for (state.planes) |p| {
            const loc = p.canvas_loc(state);
            if (rl.checkCollisionPointCircle(mouse_pos, loc, size.x / 2)) {
                return p;
            }
        }
        return null;
    }

    // returns the center position in screen-space of the plane.
    fn canvas_loc(self: Plane, state: *State) V2 {
        const rad = @as(f32, @floatFromInt(self.heading)) / 65536 * math.tau;
        const distance = state.deltaTicks * state.planeSpeed;

        const travelled = V2.init(math.sin(rad), math.cos(rad)).scale(distance);
        const interpolated = self.pos.add(travelled).multiply(flip_y);
        const in_screen_space = interpolated.add(canvas.scale(0.5)).multiply(screen).divide(canvas);

        return in_screen_space;
    }

    fn rot(self: Plane) f32 {
        const deg: f32 = @as(f32, @floatFromInt(self.heading)) / 65536;
        return deg * 360;
    }
};

const State = struct {
    now: u32 = 0,
    deltaTicks: f32 = 0,
    tickRate: u32 = 0,
    planeSpeed: f32 = 0,
    planes: []Plane = &[_]Plane{},
    mu: Thread.Mutex = .{},
    initFinished: Thread.Semaphore = .{},
    timer: std.time.Timer = undefined,
    timerBase: u32 = 0,
};

fn start_server(allocator: Allocator, proc: *Child, state: *State) !Thread {
    const thread = try Thread.spawn(.{}, read_data, .{ allocator, proc, state });
    return thread;
}

fn read_data(allocator: Allocator, proc: *Child, state: *State) !void {
    try proc.spawn();

    const out = proc.stdout orelse return;
    var buffered_state = State{};

    var init = [_]u8{0} ** (4 + 1 + 4);
    _ = try out.readAll(&init);
    state.tickRate = r_u32(init[0..4]);
    if (init[4] >= 1 << 5) {
        return error.OutOfRange;
    }
    const subPixelFactor: f32 = @floatFromInt(@as(i32, 1) << @intCast(init[4]));
    state.planeSpeed = r_f32(init[5..9]) / subPixelFactor;

    var received: u2 = 3;

    while (proc.term == null) {
        var header: [8]u8 = [_]u8{0} ** 8;
        _ = out.readAll(&header) catch break;

        const plane_count = r_u32(header[4..8]);
        const raw_planes = try allocator.alloc(u8, 16 * plane_count);
        defer allocator.free(raw_planes);

        _ = out.readAll(raw_planes) catch break;

        const now = r_u32(header[0..4]);
        buffered_state.now = now;
        if (received != 0) {
            received -= 1;
            if (received == 1) { // use the second tick to start timer because it's more reliable due to multiplayer startup lag
                state.timer = try std.time.Timer.start();
                state.timerBase = now;
                state.initFinished.post();
            }
        }

        const old = buffered_state.planes;
        buffered_state.planes = try allocator.alloc(Plane, plane_count);

        const plane_size = 4 + // id
            4 + // x
            4 + // y
            2 + // wantHeading
            2; // heading

        for (0..plane_count) |i| {
            const offset = plane_size * i;

            const b = raw_planes[offset..];
            const id = r_u32(b[0..4]);
            const x = r_f32(b[4..8]) / subPixelFactor;
            const y = r_f32(b[8..12]) / subPixelFactor;

            const want_heading = r_u16(b[12..14]);
            const heading = r_u16(b[14..16]);

            buffered_state.planes[i].id = id;
            buffered_state.planes[i].pos = V2.init(x, y);
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

const RPC = enum(u32) {
    GivePlaneHeading = 0x1,
    CommitTick = 0x8000,

    fn give_plane_heading(w: std.fs.File, plane_id: u32, start: V2, target: V2) !void {
        // convert from screen space to canvas space
        const v = target.subtract(start).multiply(flip_y);
        const h_rad = @mod(math.atan2(v.x, v.y) / math.tau, 1);
        const heading: u16 = math.lossyCast(u16, h_rad * 65536);

        var b = [_]u8{0} ** (2 + 4 + 2);
        w_u16(b[0..2], @intFromEnum(RPC.GivePlaneHeading));
        w_u32(b[2..6], plane_id);
        w_u16(b[6..8], heading);

        _ = try w.writeAll(&b);
    }
};

fn r_u16(b: *const [2]u8) u16 {
    return std.mem.readInt(u16, b, .little);
}

fn r_u32(b: *const [4]u8) u32 {
    return std.mem.readInt(u32, b, .little);
}

fn r_f32(b: *const [4]u8) f32 {
    return @floatFromInt(r_i32(b));
}

fn r_i32(b: *const [4]u8) i32 {
    return std.mem.readInt(i32, b, .little);
}

fn w_u16(b: *[2]u8, x: u16) void {
    std.mem.writeInt(u16, b, x, .little);
}

fn w_u32(b: *[4]u8, x: u32) void {
    std.mem.writeInt(u32, b, x, .little);
}

const flip_y = V2.init(1, -1);

fn rect(pos: V2, size: V2) rl.Rectangle {
    return rl.Rectangle.init(pos.x, pos.y, size.x, size.y);
}

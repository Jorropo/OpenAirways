const rl = @import("raylib");

const std = @import("std");
const print = std.debug.print;
const math = std.math;

const Thread = std.Thread;
const Allocator = std.mem.Allocator;
const Child = std.process.Child;

pub const V2 = rl.Vector2;
pub const Rect = rl.Rectangle;
pub const screen = V2.init(1280, 720);

const Game = @This();

allocator: Allocator,

server_proc: Child,
server_thread: Thread = undefined,

state: State = .{},

mu: Thread.Mutex = .{},
timer: std.time.Timer = undefined,
timer_base: u32 = 0,
read_state: ReadState = .{},

// internal game state
pub const ReadState = struct {
    // sub pixel factor used for coordinates between client and server
    sub_pixel: f32 = 0,
    // internal count for stable sync between client and server
    received_packets: u3 = 0,
    // sync main thread with server communication initialization
    init: Thread.Semaphore = .{},
};

pub const OpCode = enum(u16) {
    GivePlaneHeading = 0x0001,

    GameInit = 0x0800,
    StateUpdate = 0x0801,
    MapResize = 0x0802,
};

// the following packet sizes exclude the size of the header packet
pub const PacketSize = enum(usize) {
    GivePlaneHeading = 6,

    GameInit = 42, // NOTE: 42nd byte is size of runways.
    // StateUpdate = dynamic,
    MapResize = 16,

    const plane_size = 4 + // id
        4 + // x
        4 + // y
        2 + // wantHeading
        2; // heading

    const runway_size = 1 + // id
        4 + // x
        4 + // y
        2; // heading
};

pub fn start_server(self: *Game) !void {
    self.server_thread = try Thread.spawn(.{}, handle_inbound, .{self});
}

fn handle_inbound(self: *Game) !void {
    const proc = &self.server_proc;

    try self.server_proc.spawn();
    const out = self.server_proc.stdout orelse return;

    var header = [_]u8{0} ** 2;

    while (proc.term == null) {
        _ = out.readAll(&header) catch break;

        switch (r_u16(header[0..2])) {
            @intFromEnum(OpCode.GameInit) => self.read_init_packet() catch break,
            @intFromEnum(OpCode.StateUpdate) => self.read_state_update_packet() catch break,
            else => |v| print("error: unknown op code from server: {}\n", .{v}),
        }
    }

    self.allocator.free(self.state.planes);
    self.allocator.free(self.state.runways);
}

//
// read packate
//
// the following functions expect the first two bytes to already be read
//

fn read_init_packet(self: *Game) !void {
    const out = self.server_proc.stdout.?;

    var packet = [_]u8{0} ** @intFromEnum(Game.PacketSize.GameInit);
    _ = try out.readAll(&packet);

    self.state.tick_rate = r_u32(packet[0..4]);
    if (packet[4] >= 1 << 5) return error.OutOfRange;
    self.read_state.sub_pixel = @floatFromInt(@as(i32, 1) << @intCast(packet[4]));
    self.state.plane_speed = r_f32(packet[5..9]) / self.read_state.sub_pixel;
    self.state.map_size = r_rect(packet[9..25]);
    self.state.camera_size = r_rect(packet[25..41]);

    const runway_count = packet[41];
    const runway_bytes = try self.allocator.alloc(u8, PacketSize.runway_size * runway_count);
    defer self.allocator.free(runway_bytes);
    _ = try out.readAll(runway_bytes);

    self.state.runways = try self.allocator.alloc(Runway, runway_count);
    for (0..runway_count) |i| {
        const offset = PacketSize.runway_size * i;
        const b = runway_bytes[offset..];

        self.state.runways[i] = .{
            .id = b[0],
            .pos = .{
                .x = r_f32(b[1..5]),
                .y = r_f32(b[5..9]),
            },
            .heading = r_u16(b[9..11]),
        };
    }
}

fn read_state_update_packet(self: *Game) !void {
    const out = self.server_proc.stdout.?;

    var header = [_]u8{0} ** 8;
    _ = try out.readAll(&header);

    const current_tick = r_u32(header[0..4]);
    const plane_count = r_u32(header[4..8]);

    const plane_bytes = try self.allocator.alloc(u8, PacketSize.plane_size * plane_count);
    defer self.allocator.free(plane_bytes);
    _ = try out.readAll(plane_bytes);

    self.mu.lock();
    defer self.mu.unlock();

    if (self.read_state.received_packets < 2) {
        self.read_state.received_packets += 1;
        if (self.read_state.received_packets == 2) {
            self.timer = try std.time.Timer.start();
            self.timer_base = current_tick;
            self.read_state.init.post();
        }
    }

    self.allocator.free(self.state.planes);

    self.state.now = current_tick;
    self.state.planes = try self.allocator.alloc(Plane, plane_count);

    for (0..plane_count) |i| {
        const offset = PacketSize.plane_size * i;
        const b = plane_bytes[offset..];

        self.state.planes[i] = .{
            .id = r_u32(b[0..4]),
            .pos = .{
                .x = r_f32(b[4..8]) / self.read_state.sub_pixel,
                .y = r_f32(b[8..12]) / self.read_state.sub_pixel,
            },
            .want_heading = r_u16(b[12..14]),
            .heading = r_u16(b[14..16]),
        };
    }
}

fn read_map_resize_packet(self: *Game) !void {
    const out = self.server_proc.stdout.?;

    var packet = [_]u8{0} ** @intFromEnum(Game.PacketSize.MapResize);
    _ = try out.readAll(&packet);

    self.state.camera_size = r_rect(packet[0..16]);
}

//
// write packet
//

pub fn give_plane_heading(self: Game, plane_id: u32, start: V2, target: V2) !void {
    // convert from screen space to canvas space
    const v = target.subtract(start).multiply(flip_y);
    const h_rad = @mod(math.atan2(v.x, v.y) / math.tau, 1);
    const heading: u16 = math.lossyCast(u16, h_rad * 65536);

    var b = [_]u8{0} ** (2 + 4 + 2);
    w_u16(b[0..2], @intFromEnum(OpCode.GivePlaneHeading));
    w_u32(b[2..6], plane_id);
    w_u16(b[6..8], heading);

    _ = try self.server_proc.stdin.?.writeAll(&b);
}

pub const State = struct {
    now: u32 = 0,

    delta_ticks: f32 = 0,
    tick_rate: u32 = 0,
    plane_speed: f32 = 0,

    planes: []Plane = &[_]Plane{},
    runways: []Runway = &[_]Runway{},

    map_size: Rect = Rect.init(0, 0, 0, 0),
    camera_size: Rect = Rect.init(0, 0, 0, 0),
};

pub const Plane = struct {
    id: u32 = 0,
    pos: V2 = .{ .x = 0, .y = 0 },
    want_heading: u16 = 0,
    heading: u16 = 0,

    pub const size: V2 = .{ .x = 64, .y = 64 };

    pub fn draw(self: Plane, allocator: Allocator, state: *State, img: rl.Texture, highlight: bool, draw_debug: bool) !void {
        const center = self.current_pos(state);
        const origin = size.scale(0.5);

        img.drawPro(
            rect(V2.zero(), size),
            rect(center, size),
            origin,
            degrees(self.heading),
            rl.Color.white,
        );

        const top_left = center.subtract(origin);
        const top_right = top_left.add(V2.init(size.x, 0));

        if (highlight) {
            rl.drawRectangleRoundedLinesEx(rect(top_left, size), 64, 64, 4, rl.Color.red);
        }

        if (draw_debug) {
            const debug_text = try std.fmt.allocPrintSentinel(allocator, "id={}, pos=[{d}, {d}]", .{ self.id, self.pos.x, self.pos.y }, 0);
            rl.drawTextEx(try rl.getFontDefault(), debug_text, top_right, 16, 1, rl.Color.red);
            allocator.free(debug_text);
        }
    }

    pub fn intersecting_plane(state: *State, mouse_pos: V2, camera: rl.Camera2D) ?Plane {
        for (state.planes) |p| {
            const mouse_world_pos = rl.getScreenToWorld2D(mouse_pos, camera);
            const center = p.current_pos(state);
            if (rl.checkCollisionPointCircle(mouse_world_pos, center, size.x / 2)) {
                return p;
            }
        }
        return null;
    }

    // returns the center position in screen-space of the plane.
    pub fn current_pos(self: Plane, state: *State) V2 {
        const rad = @as(f32, @floatFromInt(self.heading)) / 65536 * math.tau;
        const distance = state.delta_ticks * state.plane_speed;

        const travelled = V2.init(math.sin(rad), math.cos(rad)).scale(distance);
        const interpolated = self.pos.add(travelled).multiply(flip_y);

        return interpolated;
    }
};

pub const Runway = struct {
    id: u8 = 0,
    pos: V2 = .{ .x = 0, .y = 0 },
    heading: u16 = 0,

    pub const size: V2 = .{ .x = 6, .y = 96 };

    const padding: f32 = 4;
    const thickness: f32 = 3;

    var i: f32 = 0;

    pub fn draw(self: Runway, camera: rl.Camera2D, highlight: bool) !void {
        const screen_pos = rl.getWorldToScreen2D(self.pos, camera);
        const rec = rect(screen_pos, size);
        rl.drawRectanglePro(rec, size.scale(0.5), degrees(self.heading), rl.Color.gray);

        if (highlight) {
            rl.gl.rlPushMatrix();
            defer rl.gl.rlPopMatrix();
            rl.gl.rlTranslatef(screen_pos.x, screen_pos.y, 0);
            rl.gl.rlRotatef(degrees(self.heading), 0, 0, 1);

            const top_left = size.scale(-0.5).subtractValue(padding + thickness);
            const highlight_size = size.addValue(padding * 2 + thickness * 2);
            const highlight_rec = rect(top_left, highlight_size);
            rl.drawRectangleLinesEx(highlight_rec, thickness, rl.Color.red);
        }
    }

    pub fn intersecting_runway(self: Runway, mouse_pos: V2, camera: rl.Camera2D) bool {
        const screen_pos = rl.getWorldToScreen2D(self.pos, camera);
        const rec = rect(screen_pos, size.addValue(padding * 2 + thickness * 2));

        return rotated_rect_intersects(rec, self.heading, mouse_pos);
    }

    pub fn closest_end(self: Runway, pos: V2, camera: rl.Camera2D) V2 {
        const rad = radians(self.heading);
        const screen_pos = self.pos;

        var v1 = V2.init(math.sin(rad), -math.cos(rad));
        v1 = v1.scale(size.y / camera.zoom / 2);

        const p1 = screen_pos.add(v1);
        const p2 = screen_pos.subtract(v1);
        const l1 = pos.subtract(p1).lengthSqr();
        const l2 = pos.subtract(p2).lengthSqr();

        const p = if (l1 < l2) p1 else p2;
        return p;
    }
};

const flip_y = V2.init(1, -1);

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

fn r_rect(b: *const [16]u8) Rect {
    const x: f32 = @floatFromInt(r_i32(b[0..4]));
    const y: f32 = @floatFromInt(r_i32(b[4..8]));
    const w: f32 = @floatFromInt(r_i32(b[8..12]));
    const h: f32 = @floatFromInt(r_i32(b[12..16]));
    return Rect.init(x, y, w, h);
}

fn w_u16(b: *[2]u8, x: u16) void {
    std.mem.writeInt(u16, b, x, .little);
}

fn w_u32(b: *[4]u8, x: u32) void {
    std.mem.writeInt(u32, b, x, .little);
}

fn degrees(n: u16) f32 {
    const deg: f32 = @as(f32, @floatFromInt(n)) / 65536;
    return deg * 360;
}

fn radians(n: u16) f32 {
    const rad: f32 = (@as(f32, @floatFromInt(n)) / 65536);
    return rad * math.tau;
}

fn rect(pos: V2, size: V2) Rect {
    return Rect.init(pos.x, pos.y, size.x, size.y);
}

// this function assumes rec.x and rec.y are the center of the rectangle.
//
// get points of rect:
// https://stackoverflow.com/a/56848101
// get whether intersects:
// https://swharden.com/blog/2022-02-01-point-in-rectangle/
//
fn rotated_rect_intersects(rec: Rect, angle: u16, p: V2) bool {
    const rad = radians(angle);

    var v1 = V2.init(math.sin(rad), -math.cos(rad));
    var v2 = V2{ .x = -v1.y, .y = v1.x }; // perpendicular to v1
    v1 = v1.scale(rec.height / 2);
    v2 = v2.scale(rec.width / 2);

    const center = V2.init(rec.x, rec.y);

    // corners of the rotated rectangle
    const a = center.add(v1).add(v2);
    const b = center.add(v1).subtract(v2);
    const c = center.subtract(v1).subtract(v2);
    const d = center.subtract(v1).add(v2);

    const apb = triangle_area(a, p, b);
    const bpc = triangle_area(b, p, c);
    const cpd = triangle_area(c, p, d);
    const dpa = triangle_area(d, p, a);

    return apb + bpc + cpd + dpa <= rec.width * rec.height + 0.2;
}

fn triangle_area(a: V2, b: V2, c: V2) f32 {
    const sum = a.x * (b.y - c.y) + b.x * (c.y - a.y) + c.x * (a.y - b.y);
    return @abs(sum) * 0.5;
}

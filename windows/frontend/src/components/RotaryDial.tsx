import React, { useEffect, useRef, useState } from "react";
import { Power } from "lucide-react";
import type { Phase } from "../types";

// Exact geometry from DialGlyph.swift & DialControl.swift (16-unit reference grid, diameter = 112px)
const DISC_DIAMETER = 112;
const RING_INSET = 9;
const OUTER_DIAMETER = DISC_DIAMETER + RING_INSET * 2; // 130px
const SCALE = DISC_DIAMETER / 13.2; // 8.4848 px/unit
const CENTER = OUTER_DIAMETER / 2; // 65px

const HOLE_ORBIT = 4.65 * SCALE; // 39.45px
const HOLE_RADIUS = 1.1 * SCALE; // 9.33px (diameter 18.67px)
const HUB_RADIUS = 2.4 * SCALE; // 20.36px (diameter 40.73px)

const STOP_ANGLE_DEG = 47.5;
const STOP_INNER = 4.03 * SCALE; // 34.19px
const STOP_OUTER = 5.65 * SCALE; // 47.94px
const STOP_STROKE_WIDTH = 1.2 * SCALE; // 10.18px

// Exact computed angles and clockwise max travels for the 6 holes
const HOLE_ANGLES_DEG = [299.71, 256.95, 214.19, 171.44, 128.68, 85.93];
const HOLE_TRAVELS_DEG = [84.75, 127.51, 170.27, 213.02, 255.78, 298.53];
const AUTO_DIAL_SEQUENCE = [1, 0, 3, 2, 0, 4];
const RETURN_SPEED_DEG_PER_SEC = 330;

function degToRad(deg: number): number {
  return (deg * Math.PI) / 180;
}

function normalizeDeltaDeg(delta: number): number {
  let d = delta % 360;
  if (d > 180) d -= 360;
  if (d < -180) d += 360;
  return d;
}

export interface RotaryDialProps {
  phase: Phase;
  isProxyOnly: boolean;
  disabled?: boolean;
  onTrigger: () => void;
}

export const RotaryDial: React.FC<RotaryDialProps> = ({
  phase,
  isProxyOnly,
  disabled = false,
  onTrigger,
}) => {
  const svgRef = useRef<SVGSVGElement | null>(null);
  const [rotation, setRotation] = useState(0);
  const [activeHole, setActiveHole] = useState<number | null>(null);
  const [pressed, setPressed] = useState(false);

  const rotationRef = useRef(0);
  const dragStateRef = useRef<{
    pointerId: number;
    holeIndex: number | null;
    startX: number;
    startY: number;
    lastAngle: number;
    dragged: boolean;
  } | null>(null);

  const isWorking = phase === "connecting" || phase === "switching" || phase === "disconnecting";
  const isConnected = phase === "connected";
  const isFailed = phase === "failed";
  const isLit = isConnected || isFailed;

  const stateColor = isFailed
    ? "#D98C7D"
    : isConnected
      ? isProxyOnly
        ? "#B3AAD8"
        : "#91AD98"
      : "#A2B3BC";

  // Automatic dialing animation while working + smooth governor unwind when exiting working
  useEffect(() => {
    let rafId = 0;
    let cancelled = false;

    if (isWorking) {
      let stepIdx = 0;
      let stage: "gap" | "pull" | "hold" | "back" = "gap";
      let stageStart = performance.now();
      let currentTarget = 0;

      const tick = (now: number) => {
        if (cancelled) return;
        const elapsed = (now - stageStart) / 1000;
        const hole = AUTO_DIAL_SEQUENCE[stepIdx % AUTO_DIAL_SEQUENCE.length];

        if (stage === "gap") {
          if (elapsed >= 0.16) {
            setActiveHole(hole);
            currentTarget = HOLE_TRAVELS_DEG[hole];
            stage = "pull";
            stageStart = now;
          }
        } else if (stage === "pull") {
          const pullDuration = 0.22 + currentTarget / 520;
          const p = Math.min(1, elapsed / pullDuration);
          const smooth = p * p * (3 - 2 * p);
          const nextRot = currentTarget * smooth;
          rotationRef.current = nextRot;
          setRotation(nextRot);
          if (p >= 1) {
            stage = "hold";
            stageStart = now;
          }
        } else if (stage === "hold") {
          if (elapsed >= 0.1) {
            setActiveHole(null);
            stage = "back";
            stageStart = now;
          }
        } else if (stage === "back") {
          const nextRot = Math.max(0, currentTarget - elapsed * RETURN_SPEED_DEG_PER_SEC);
          rotationRef.current = nextRot;
          setRotation(nextRot);
          if (nextRot <= 0.01) {
            rotationRef.current = 0;
            setRotation(0);
            stepIdx++;
            stage = "gap";
            stageStart = now;
          }
        }
        rafId = requestAnimationFrame(tick);
      };
      rafId = requestAnimationFrame(tick);
      return () => {
        cancelled = true;
        cancelAnimationFrame(rafId);
      };
    } else {
      // Smooth unwind to 0 at 330 deg/s if currently rotated
      setActiveHole(null);
      let lastTime = performance.now();
      const unwind = (now: number) => {
        if (cancelled) return;
        if (dragStateRef.current?.dragged) return;
        const cur = rotationRef.current;
        if (cur <= 0.05) {
          if (cur !== 0) {
            rotationRef.current = 0;
            setRotation(0);
          }
          return;
        }
        const dt = (now - lastTime) / 1000;
        lastTime = now;
        const next = Math.max(0, cur - dt * RETURN_SPEED_DEG_PER_SEC);
        rotationRef.current = next;
        setRotation(next);
        rafId = requestAnimationFrame(unwind);
      };
      if (rotationRef.current > 0) {
        rafId = requestAnimationFrame(unwind);
      }
      return () => {
        cancelled = true;
        cancelAnimationFrame(rafId);
      };
    }
  }, [isWorking]);

  const pointerAngleAndCoords = (clientX: number, clientY: number) => {
    const rect = svgRef.current?.getBoundingClientRect();
    if (!rect) return { x: 0, y: 0, r: 0, angle: 0 };
    const x = clientX - rect.left - CENTER;
    const y = clientY - rect.top - CENTER;
    const r = Math.hypot(x, y);
    const angle = ((Math.atan2(y, x) * 180) / Math.PI + 360) % 360;
    return { x, y, r, angle };
  };

  const handlePointerDown = (e: React.PointerEvent<SVGSVGElement>) => {
    if (disabled) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    setPressed(true);

    const { x, y, angle } = pointerAngleAndCoords(e.clientX, e.clientY);
    let grabbedHole: number | null = null;

    if (!isWorking) {
      for (let i = 0; i < 6; i++) {
        const rad = degToRad(HOLE_ANGLES_DEG[i] + rotationRef.current);
        const hx = Math.cos(rad) * HOLE_ORBIT;
        const hy = Math.sin(rad) * HOLE_ORBIT;
        if (Math.hypot(x - hx, y - hy) <= HOLE_RADIUS * 1.5) {
          grabbedHole = i;
          setActiveHole(i);
          break;
        }
      }
    }

    dragStateRef.current = {
      pointerId: e.pointerId,
      holeIndex: grabbedHole,
      startX: e.clientX,
      startY: e.clientY,
      lastAngle: angle,
      dragged: false,
    };
  };

  const handlePointerMove = (e: React.PointerEvent<SVGSVGElement>) => {
    const st = dragStateRef.current;
    if (!st || st.pointerId !== e.pointerId) return;

    const movedDist = Math.hypot(e.clientX - st.startX, e.clientY - st.startY);
    if (!st.dragged && movedDist > 8 && st.holeIndex !== null && !isWorking) {
      st.dragged = true;
      setPressed(false);
    }

    if (st.dragged && st.holeIndex !== null && !isWorking) {
      const { r, angle } = pointerAngleAndCoords(e.clientX, e.clientY);
      if (r < HUB_RADIUS * 0.5) return;
      const delta = normalizeDeltaDeg(angle - st.lastAngle);
      st.lastAngle = angle;
      const maxTravel = HOLE_TRAVELS_DEG[st.holeIndex];
      const next = Math.max(0, Math.min(maxTravel, rotationRef.current + delta));
      rotationRef.current = next;
      setRotation(next);
    }
  };

  const finishPointer = (e: React.PointerEvent<SVGSVGElement>, cancelled: boolean) => {
    const st = dragStateRef.current;
    if (!st || st.pointerId !== e.pointerId) return;
    const wasDragged = st.dragged;
    dragStateRef.current = null;
    setPressed(false);
    setActiveHole(null);

    if (wasDragged) {
      // Unwind smoothly back to 0 at 330 deg/s without firing click
      let last = performance.now();
      const step = (now: number) => {
        if (dragStateRef.current?.dragged) return;
        const cur = rotationRef.current;
        if (cur <= 0.05) {
          rotationRef.current = 0;
          setRotation(0);
          return;
        }
        const dt = (now - last) / 1000;
        last = now;
        const next = Math.max(0, cur - dt * RETURN_SPEED_DEG_PER_SEC);
        rotationRef.current = next;
        setRotation(next);
        requestAnimationFrame(step);
      };
      requestAnimationFrame(step);
      return;
    }

    if (!cancelled && !disabled) {
      onTrigger();
    }
  };

  const stopRad = degToRad(STOP_ANGLE_DEG);
  const stopX1 = CENTER + Math.cos(stopRad) * STOP_INNER;
  const stopY1 = CENTER + Math.sin(stopRad) * STOP_INNER;
  const stopX2 = CENTER + Math.cos(stopRad) * STOP_OUTER;
  const stopY2 = CENTER + Math.sin(stopRad) * STOP_OUTER;

  const outerRingRadius = (OUTER_DIAMETER - 2.5) / 2;
  const ringCircumference = 2 * Math.PI * outerRingRadius;

  return (
    <div
      style={{
        position: "relative",
        width: OUTER_DIAMETER,
        height: OUTER_DIAMETER,
        flexShrink: 0,
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        opacity: disabled ? 0.55 : 1,
        transform: pressed ? "scale(0.955)" : "scale(1)",
        transition: "transform 0.18s cubic-bezier(0.34, 1.56, 0.64, 1), opacity 0.15s ease",
        cursor: disabled ? "not-allowed" : "pointer",
      }}
    >
      {/* Ambient Halo */}
      <div
        style={{
          position: "absolute",
          width: DISC_DIAMETER * 1.35,
          height: DISC_DIAMETER * 1.35,
          borderRadius: "50%",
          background: stateColor,
          opacity: isLit ? 0.28 : isWorking ? 0.2 : 0.1,
          filter: "blur(18px)",
          pointerEvents: "none",
          transition: "background 0.25s ease, opacity 0.25s ease",
        }}
      />

      <svg
        ref={svgRef}
        width={OUTER_DIAMETER}
        height={OUTER_DIAMETER}
        viewBox={`0 0 ${OUTER_DIAMETER} ${OUTER_DIAMETER}`}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={(e) => finishPointer(e, false)}
        onPointerCancel={(e) => finishPointer(e, true)}
        style={{ overflow: "visible", touchAction: "none" }}
      >
        {/* Outer Status Ring */}
        <circle
          cx={CENTER}
          cy={CENTER}
          r={outerRingRadius}
          fill="none"
          stroke={isLit ? stateColor : "rgba(255, 255, 255, 0.28)"}
          strokeWidth={2.5}
          style={{
            filter: isLit ? `drop-shadow(0 0 6px ${stateColor})` : undefined,
            transition: "stroke 0.22s ease",
          }}
        />

        {/* Rotating 94-deg Sweep Arc while Working */}
        {isWorking && (
          <g style={{ transformOrigin: `${CENTER}px ${CENTER}px` }} className="xd-spin">
            <circle
              cx={CENTER}
              cy={CENTER}
              r={outerRingRadius}
              fill="none"
              stroke={stateColor}
              strokeWidth={2.8}
              strokeLinecap="round"
              strokeDasharray={`${ringCircumference * 0.26} ${ringCircumference}`}
            />
          </g>
        )}

        {/* Main Rotary Disc */}
        <circle
          cx={CENTER}
          cy={CENTER}
          r={DISC_DIAMETER / 2}
          fill={isLit ? stateColor : "rgba(255, 255, 255, 0.11)"}
          stroke="rgba(255, 255, 255, 0.16)"
          strokeWidth={0.8}
          style={{ transition: "fill 0.22s ease" }}
        />

        {/* 6 Rotating Finger Holes */}
        {HOLE_ANGLES_DEG.map((baseAngle, idx) => {
          const rad = degToRad(baseAngle + rotation);
          const hx = CENTER + Math.cos(rad) * HOLE_ORBIT;
          const hy = CENTER + Math.sin(rad) * HOLE_ORBIT;
          const isActive = activeHole === idx;

          let holeFill = "rgba(0, 0, 0, 0.30)";
          if (isLit) {
            holeFill = isActive ? "rgba(255, 255, 255, 0.72)" : "rgba(255, 255, 255, 0.94)";
          } else if (isActive) {
            holeFill = stateColor;
          }

          return (
            <circle
              key={idx}
              cx={hx}
              cy={hy}
              r={HOLE_RADIUS}
              fill={holeFill}
              stroke={isLit ? "none" : "rgba(255, 255, 255, 0.10)"}
              strokeWidth={0.75}
              style={{
                filter:
                  isLit || isActive
                    ? `drop-shadow(0 0 3px ${isLit ? "rgba(255,255,255,0.35)" : stateColor})`
                    : undefined,
              }}
            />
          );
        })}

        {/* Stationary Finger Stop (with separator under-stroke when lit) */}
        {isLit && (
          <line
            x1={stopX1}
            y1={stopY1}
            x2={stopX2}
            y2={stopY2}
            stroke={stateColor}
            strokeWidth={STOP_STROKE_WIDTH + 5}
            strokeLinecap="round"
          />
        )}
        <line
          x1={stopX1}
          y1={stopY1}
          x2={stopX2}
          y2={stopY2}
          stroke={isLit ? "rgba(255, 255, 255, 0.94)" : "rgba(255, 255, 255, 0.42)"}
          strokeWidth={STOP_STROKE_WIDTH}
          strokeLinecap="round"
        />

        {/* Stationary Center Power Hub */}
        <circle
          cx={CENTER}
          cy={CENTER}
          r={HUB_RADIUS}
          fill={isLit ? "rgba(255, 255, 255, 0.22)" : "rgba(255, 255, 255, 0.10)"}
          stroke={isLit ? "rgba(255, 255, 255, 0.52)" : "rgba(255, 255, 255, 0.32)"}
          strokeWidth={1}
        />
      </svg>

      {/* Center Power Icon */}
      <div
        style={{
          position: "absolute",
          pointerEvents: "none",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          color: "#FFFFFF",
        }}
      >
        <Power size={17} strokeWidth={2.3} />
      </div>
    </div>
  );
};
